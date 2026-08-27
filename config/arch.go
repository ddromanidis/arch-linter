// Package config reads the two files archlint is driven by.
//
// They are deliberately separate. arch.yaml is the architecture: a design document that
// happens to be executable, reviewed like code and diffed like code. arch.config.yaml is
// how the tool behaves — output format, exclusions, severities — which is operational
// noise that would otherwise pollute the design document every time CI changed.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Arch is the parsed arch.yaml: what the components are and what each may reach.
type Arch struct {
	Version int `yaml:"version"`
	// Module is the Go module path. Optional; when empty it is read from go.mod, which is
	// what it would have said anyway.
	Module     string               `yaml:"module"`
	Components map[string]Component `yaml:"components"`
	Defaults   Defaults             `yaml:"defaults"`

	// lines maps a component to the line it is declared on, so a diagnostic about the
	// architecture can point at the architecture.
	lines map[string]int
}

// ComponentNames returns every declared component, sorted.
func (a *Arch) ComponentNames() []string {
	out := make([]string, 0, len(a.Components))
	for name := range a.Components {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ComponentLine is the line in arch.yaml where a component is declared, or 1 if unknown.
func (a *Arch) ComponentLine(name string) int {
	if n, ok := a.lines[name]; ok && n > 0 {
		return n
	}
	return 1
}

// Component is one named region of the codebase.
//
// Rules are allowlists rather than denylists. A denylist is only as good as your
// imagination on the day you wrote it: the dependency that hurts is the one nobody thought
// to ban. An allowlist is closed by default, so every new edge into a component is a
// deliberate edit to this file, which is exactly the moment the question "should this
// depend on that?" is worth asking.
type Component struct {
	// Path and Paths are the same thing; Path is sugar for the common single-pattern case.
	// A trailing "/..." matches the package and everything beneath it, as in go tooling.
	Path        string   `yaml:"path"`
	Paths       []string `yaml:"paths"`
	Description string   `yaml:"description"`

	// Imports is what this component may import.
	//
	// Omitting the key and writing `imports: []` are different statements. The empty list
	// says this component may depend on nothing; omitting it says nothing at all about the
	// component, and no import rule is applied. That is what lets the tool be adopted a
	// component at a time rather than all at once.
	Imports []string `yaml:"imports"`
	// Exports is what may appear in this component's exported API surface — the rule no
	// other Go architecture linter has. A component may legitimately *import* its database
	// driver and still have no business *returning* it.
	//
	// Omitted means unconstrained, as with Imports. Most components have no API surface
	// worth restricting, and requiring every one of them to say so was noise.
	Exports []string `yaml:"exports"`
	// Severity overrides the global severity for this component only.
	//
	// What makes a rule adoptable layer by layer: `domain` at error because it is already
	// clean, `adapters` at warning because it is aspirational. A baseline can say "not
	// these existing violations"; only this can say "this layer is a goal, not yet a
	// promise", which is a different and often more honest statement.
	//
	// Lives here rather than in arch.config.yaml because it is a claim about a component,
	// and belongs beside the rules it modifies.
	Severity *Severities `yaml:"severity"`
	// Deny bans packages outright, for both importing and exporting, whatever the
	// allowlists say.
	//
	// The allowlists already deny by default, so this exists for the case they cannot
	// reach: narrowing something otherwise permitted. `imports: [std]` with
	// `deny: [os/exec]` is the whole standard library minus the one package you have
	// decided nobody may shell out through — and it keeps working on an unconstrained
	// component, which is the only rule that does.
	Deny []string `yaml:"deny"`
}

// Defaults apply to every component, so that "everyone may use fmt" is stated once.
type Defaults struct {
	Imports []string `yaml:"imports"`
	Exports []string `yaml:"exports"`
	// Deny bans packages across every component. Project-wide bans — `unsafe`, a
	// deprecated internal library, the JSON package you have standardised away from —
	// belong here rather than repeated in each component.
	Deny []string `yaml:"deny"`
}

// patterns returns every path pattern for a component, in declaration order.
func (c Component) patterns() []string {
	var out []string
	if c.Path != "" {
		out = append(out, c.Path)
	}
	return append(out, c.Paths...)
}

// ParseArch reads and validates an arch.yaml.
func ParseArch(file string) (*Arch, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	return parseArch(data, file)
}

func parseArch(data []byte, file string) (*Arch, error) {
	var a Arch
	dec := yaml.NewDecoder(bytes.NewReader(data))
	// Unknown keys are errors, not shrugs.
	//
	// v1 shipped a config the parser could not read: it expected `global.imports.allow`
	// while its own example wrote `global.allow`, so the example's project-wide rules
	// parsed to nothing and the linter reported a clean run it had not earned. A typo in
	// an architecture rule must never be indistinguishable from an absent one.
	dec.KnownFields(true)
	if err := dec.Decode(&a); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if err := a.validate(file); err != nil {
		return nil, err
	}
	// A second, non-strict pass purely for positions.
	//
	// Line numbers could have come from the first pass by making Component a
	// yaml.Unmarshaler, but node-level decoding does not honour KnownFields, so unknown
	// keys inside a component would stop being errors. That check is worth more than one
	// convenient traversal: it is the whole reason the v1 config bug cannot recur here.
	a.lines = componentLines(data)
	return &a, nil
}

// componentLines finds the line each component is declared on. Best effort — the strict
// pass has already established the file is valid, so a failure here costs a position, not
// a diagnostic.
func componentLines(data []byte) map[string]int {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil
	}
	if len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	out := map[string]int{}
	// A mapping node alternates key, value, key, value.
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "components" {
			continue
		}
		components := root.Content[i+1]
		for j := 0; j+1 < len(components.Content); j += 2 {
			out[components.Content[j].Value] = components.Content[j].Line
		}
	}
	return out
}

func (a *Arch) validate(file string) error {
	if a.Version != 1 {
		return fmt.Errorf("%s: version must be 1, got %d", file, a.Version)
	}
	if len(a.Components) == 0 {
		return fmt.Errorf("%s: no components defined", file)
	}

	// Deterministic order, so two runs on the same broken file report the same first error.
	names := make([]string, 0, len(a.Components))
	for name := range a.Components {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		c := a.Components[name]
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("%s: component with an empty name", file)
		}
		if len(c.patterns()) == 0 {
			return fmt.Errorf("%s: component %q has no path", file, name)
		}
		for _, p := range c.patterns() {
			if strings.TrimSpace(p) == "" {
				return fmt.Errorf("%s: component %q has an empty path", file, name)
			}
			if strings.Contains(p, "...") && !strings.HasSuffix(p, "/...") {
				return fmt.Errorf(
					"%s: component %q path %q: %q is only meaningful as a trailing %q",
					file, name, p, "...", "/...")
			}
		}
		// A rule naming a component that does not exist is always a mistake — usually a
		// rename that missed a reference — and silently treating it as an import path
		// would turn it into a rule that can never match.
		for _, kind := range []struct {
			what  string
			rules []string
		}{{"imports", c.Imports}, {"exports", c.Exports}} {
			for _, r := range kind.rules {
				if err := a.checkRule(file, name, kind.what, r); err != nil {
					return err
				}
			}
		}
		if c.Severity != nil {
			for rule, sev := range map[string]Severity{
				"imports": c.Severity.Imports, "exports": c.Severity.Exports,
				"waivers": c.Severity.Waivers, "cycles": c.Severity.Cycles,
				"coverage": c.Severity.Coverage, "unclassified": c.Severity.Unclassified,
			} {
				if sev != "" && !sev.valid() {
					return fmt.Errorf(
						"%s: component %q: severity.%s must be error, warning or off, got %q",
						file, name, rule, sev)
				}
			}
		}
	}

	for _, kind := range []struct {
		what  string
		rules []string
	}{
		{"imports", a.Defaults.Imports},
		{"exports", a.Defaults.Exports},
		{"deny", a.Defaults.Deny},
	} {
		for _, r := range kind.rules {
			if err := a.checkRule(file, "defaults", kind.what, r); err != nil {
				return err
			}
		}
	}
	return nil
}

// StdKeyword allows the entire standard library in one rule entry.
const StdKeyword = "std"

// isStdlib reports whether an import path names a standard library package.
//
// Uses the language's own rule — a module path's first element must contain a dot — rather
// than a list of known roots. A list goes stale: this was hardcoded, and Go 1.27 added
// `uuid` to the standard library, so a real codebase importing it was told the standard
// library was a third-party dependency it had not declared. The dot rule cannot rot,
// because it is the rule the go command itself uses to tell the two apart.
func isStdlib(importPath string) bool {
	first, _, _ := strings.Cut(importPath, "/")
	return first != "" && !strings.Contains(first, ".")
}

// checkRule rejects entries that can never match anything.
func (a *Arch) checkRule(file, owner, what, rule string) error {
	if strings.TrimSpace(rule) == "" {
		return fmt.Errorf("%s: %s.%s has an empty entry", file, owner, what)
	}
	if strings.Contains(rule, "...") && !strings.HasSuffix(rule, "/...") {
		return fmt.Errorf("%s: %s.%s: %q is only meaningful as a trailing %q",
			file, owner, what, "...", "/...")
	}
	if rule == StdKeyword {
		return nil
	}
	if _, isComponent := a.Components[rule]; isComponent {
		return nil
	}
	// Not a component name, so it has to be an import path. Anything with a separator or a
	// dot plainly is one (net/http, gorm.io/gorm). What is left is a single bare word,
	// which is ambiguous: `fmt` is the standard library, but `doamin` is a component name
	// somebody has fat-fingered. Only the former is real, so check the list.
	base := strings.TrimSuffix(rule, "/...")
	if strings.Contains(base, "/") || strings.Contains(base, ".") {
		return nil
	}
	if stdlibRoots[base] {
		return nil
	}
	return fmt.Errorf(
		"%s: %s.%s: %q is neither a component nor an import path",
		file, owner, what, rule)
}

// stdlibRoots exists only to catch typos in a config file, and is deliberately not used to
// decide what the standard library is at analysis time — see isStdlib.
//
// The distinction matters. A bare word in a rule list is ambiguous: `fmt` is the standard
// library, `doamin` is a fat-fingered component name. Checking against known roots catches
// the second. Being out of date here costs a false error on a config entry, which is
// visible and fixable; being out of date in isStdlib cost a hundred false findings against
// real code, which is neither.
var stdlibRoots = map[string]bool{
	"archive": true, "bufio": true, "builtin": true, "bytes": true, "cmp": true,
	"compress": true, "container": true, "context": true, "crypto": true,
	"database": true, "debug": true, "embed": true, "encoding": true, "errors": true,
	"expvar": true, "flag": true, "fmt": true, "go": true, "hash": true, "html": true,
	"image": true, "index": true, "io": true, "iter": true, "log": true, "maps": true,
	"math": true, "mime": true, "net": true, "os": true, "path": true, "plugin": true,
	"reflect": true, "regexp": true, "runtime": true, "slices": true, "sort": true,
	"strconv": true, "strings": true, "structs": true, "sync": true, "syscall": true,
	"testing": true, "text": true, "time": true, "unicode": true, "unique": true,
	"unsafe": true, "uuid": true, "weak": true,
}

// ModulePath returns the module path from a go.mod, for when arch.yaml omits it.
func ModulePath(goMod string) (string, error) {
	data, err := os.ReadFile(goMod)
	if err != nil {
		return "", err
	}
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module"); ok {
			if p := strings.TrimSpace(rest); p != "" {
				return strings.Trim(p, `"`), nil
			}
		}
	}
	return "", fmt.Errorf("%s: no module directive", goMod)
}

// Qualify turns a pattern written relative to the module into a full import path. Exported
// because exclusion patterns in arch.config.yaml are written the same way component paths
// are, and must be resolved the same way — they were not, so a relative exclude silently
// matched nothing.
func Qualify(module, pattern string) string { return qualify(module, pattern) }

// qualify turns a pattern written relative to the module into a full import path.
//
// Patterns are written relative because that is how the architecture reads: "internal/domain"
// is the component, and repeating github.com/you/project in front of every one of them adds
// length without adding meaning. A pattern that already names another module is left alone.
func qualify(module, pattern string) string {
	if module == "" {
		return pattern
	}
	base := strings.TrimSuffix(pattern, "/...")
	if base == module || strings.HasPrefix(base, module+"/") {
		return pattern
	}
	// A dot in the first segment means a domain name, so this is somebody else's module.
	if first, _, _ := strings.Cut(base, "/"); strings.Contains(first, ".") {
		return pattern
	}
	return path.Join(module, pattern)
}

// qualifyRule is qualify for rule entries rather than component paths.
//
// The two differ on one point: a component path is always somewhere in this repository, so
// `internal/domain` means `<module>/internal/domain`. A rule entry may equally name the
// standard library, where `fmt` means `fmt` and emphatically not `<module>/fmt`.
//
// The ambiguity is real but narrow — it only bites a repository with a top-level directory
// named after a standard library root, such as `os/`, which would need writing out in full.
func qualifyRule(module, rule string) string {
	base := strings.TrimSuffix(rule, "/...")
	if first, _, _ := strings.Cut(base, "/"); stdlibRoots[first] {
		return rule
	}
	return qualify(module, rule)
}
