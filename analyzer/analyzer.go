// Package analyzer is arch-lint itself, as a golang.org/x/tools/go/analysis.Analyzer.
//
// Being an Analyzer rather than a bespoke driver is the load-bearing decision of the whole
// design, and it works because architecture linting turns out to be package-local: a
// package's imports are its own, and its exported surface is its own. The only global input
// is the configuration, which is static. So no analysis.Fact propagation is needed, the
// framework fits without being fought, and every way of running the linter — the CLI,
// `go vet -vettool`, and the golangci-lint plugin — is a thin wrapper over this one
// implementation instead of three drifting copies of it.
package analyzer

import (
	"fmt"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"github.com/ddromanidis/arch-linter/config"
	"golang.org/x/tools/go/analysis"
)

// Rules is everything the checks need: the compiled architecture and the tool settings.
type Rules struct {
	Resolver *config.Resolver
	Config   *config.Config
}

// skipPackage reports whether a package is excluded from analysis entirely.
func (r *Rules) skipPackage(importPath string) bool {
	for _, pattern := range r.Config.Exclude {
		if strings.HasSuffix(pattern, ".go") {
			continue // a file glob, handled by skipFile
		}
		base := strings.TrimSuffix(pattern, "/...")
		if importPath == base || strings.HasPrefix(importPath, base+"/") {
			return true
		}
	}
	return false
}

// skipFile reports whether one file is excluded.
//
// Test files are excluded unless asked for, because tests routinely and correctly reach
// across boundaries their production code may not — a fake repository in a domain test is
// the point, not a violation — and a linter that fails on that is a linter people switch
// off entirely rather than argue with.
func (r *Rules) skipFile(filename string) bool {
	base := filepath.Base(filename)
	if !r.Config.IncludeTests && strings.HasSuffix(base, "_test.go") {
		return true
	}
	for _, pattern := range r.Config.Exclude {
		if !strings.HasSuffix(pattern, ".go") {
			continue
		}
		// Globs are matched against the base name, so `mock_*.go` works without every
		// config having to know how deep the directory tree is. A leading `**/` is
		// accepted and ignored, since it is what people write out of habit.
		if ok, _ := path.Match(strings.TrimPrefix(pattern, "**/"), base); ok {
			return true
		}
	}
	return false
}

// New builds an Analyzer bound to rules already loaded. This is the path the CLI takes:
// it reads the config once and hands it over, so nothing is discovered twice.
func New(rules *Rules) *analysis.Analyzer {
	return &analysis.Analyzer{
		Name:       "archlint",
		Doc:        doc,
		ResultType: reflect.TypeOf([]Violation{}),
		Run: func(pass *analysis.Pass) (any, error) {
			return run(pass, rules)
		},
	}
}

const doc = `check that package dependencies and exported API surfaces obey arch.yaml

Two rules. "imports" is the familiar one: a component may only import what it declares.
"exports" is the reason this tool exists: a component may only expose declared packages in
its exported API. A repository may import its database driver and still have no business
returning it, and no amount of import checking will notice the difference.`

// Analyzer is the flag-driven Analyzer, for `go vet -vettool` and golangci-lint, where
// there is no opportunity to pass anything in. Configuration is discovered by walking up
// from the package being analysed, and cached across packages.
var Analyzer = newDiscovering()

var (
	archFlag   string
	configFlag string

	cacheMu sync.Mutex
	cache   = map[string]*Rules{}
)

func newDiscovering() *analysis.Analyzer {
	a := &analysis.Analyzer{
		Name:       "archlint",
		Doc:        doc,
		ResultType: reflect.TypeOf([]Violation{}),
		Run: func(pass *analysis.Pass) (any, error) {
			rules, err := discover(pass, archFlag, configFlag)
			if err != nil {
				return nil, err
			}
			if rules == nil {
				// No arch.yaml anywhere above this package. Not an error: a repository is
				// allowed to have corners the architecture says nothing about.
				return []Violation(nil), nil
			}
			return run(pass, rules)
		},
	}
	a.Flags.StringVar(&archFlag, "arch", "", "path to arch.yaml (default: found by walking up)")
	a.Flags.StringVar(&configFlag, "config", "", "path to arch.config.yaml")
	return a
}

// discover finds and caches the configuration governing a package.
func discover(pass *analysis.Pass, archPath, configPath string) (*Rules, error) {
	if archPath == "" {
		dir := packageDir(pass)
		if dir == "" {
			return nil, nil
		}
		found, ok := findUp(dir, "arch.yaml")
		if !ok {
			return nil, nil
		}
		archPath = found
	}

	cacheMu.Lock()
	defer cacheMu.Unlock()
	if r, ok := cache[archPath]; ok {
		return r, nil
	}

	rules, err := Load(archPath, configPath)
	if err != nil {
		return nil, err
	}
	cache[archPath] = rules
	return rules, nil
}

// Load reads an arch.yaml and its companion config, filling in the module path from the
// neighbouring go.mod when arch.yaml does not state it.
//
// configPath may be empty, in which case arch.config.yaml is looked for beside arch.yaml
// and its absence means the defaults.
func Load(archPath, configPath string) (*Rules, error) {
	arch, err := config.ParseArch(archPath)
	if err != nil {
		return nil, err
	}

	root := filepath.Dir(archPath)
	module := arch.Module
	if module == "" {
		goMod, ok := findUp(root, "go.mod")
		if !ok {
			return nil, fmt.Errorf(
				"%s: no `module:` and no go.mod found above it — one of the two has to say",
				archPath)
		}
		if module, err = config.ModulePath(goMod); err != nil {
			return nil, err
		}
	}

	if configPath == "" {
		configPath = filepath.Join(root, "arch.config.yaml")
	}
	cfg, err := config.ParseConfig(configPath)
	if err != nil {
		return nil, err
	}

	return &Rules{Resolver: config.NewResolver(arch, module), Config: cfg}, nil
}

// packageDir returns the directory holding the package under analysis.
func packageDir(pass *analysis.Pass) string {
	for _, f := range pass.Files {
		if name := pass.Fset.File(f.Pos()).Name(); name != "" {
			return filepath.Dir(name)
		}
	}
	return ""
}

// findUp looks for name in dir and every parent.
func findUp(dir, name string) (string, bool) {
	for {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// run applies both rules to one package.
func run(pass *analysis.Pass, rules *Rules) (any, error) {
	// The import path of a test variant is decorated — "p [p.test]" — and the architecture
	// has nothing to say about a synthetic package.
	pkgPath := strings.Fields(pass.Pkg.Path())[0]

	if rules.skipPackage(pkgPath) {
		return []Violation(nil), nil
	}
	component := rules.Resolver.Component(pkgPath)
	if component == "" {
		// Outside the architecture. Reporting every unclassified package would make
		// adopting the tool on a real repository an exercise in silencing it; a package
		// nobody has assigned to a component simply has no rules yet.
		return []Violation(nil), nil
	}

	c := &checker{
		pass:      pass,
		rules:     rules,
		component: component,
		waivers:   collectWaivers(pass),
	}
	if rules.Config.Severity.Imports != config.Off {
		c.checkImports()
	}
	if rules.Config.Severity.Exports != config.Off {
		c.checkExports()
	}
	// Last, so that "used" is accurate: a waiver is only unused once both rules have had
	// their chance to be silenced by it.
	c.reportWaiverProblems()
	return c.found, nil
}

// inSkippedFile reports whether a position falls in an excluded file.
func inSkippedFile(pass *analysis.Pass, rules *Rules, pos token.Pos) bool {
	f := pass.Fset.File(pos)
	if f == nil {
		return false
	}
	return rules.skipFile(f.Name())
}

// Rule categories, carried on every Diagnostic so a driver can tell the two apart without
// reading the message text.
const (
	RuleImports = "imports"
	RuleExports = "exports"
	RuleWaivers = "waivers"
)

// Violation is the structured form of a diagnostic, returned as the analyzer's result so a
// driver does not have to parse message text to learn what was violated.
type Violation struct {
	Rule      string
	Component string
	// Target is the import path the rule was broken about — the stable half of a
	// violation's identity. Messages get reworded; package paths do not.
	Target  string
	Message string
	Pos     token.Pos
}
