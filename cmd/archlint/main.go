// Command archlint checks that a Go module obeys the architecture written in its
// arch.yaml — what each component may import, and what each component may expose.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ddromanidis/arch-linter/analyzer"
	"github.com/ddromanidis/arch-linter/config"
	"github.com/ddromanidis/arch-linter/internal/baseline"
	"github.com/ddromanidis/arch-linter/internal/diagram"
	"github.com/ddromanidis/arch-linter/internal/report"
	"github.com/ddromanidis/arch-linter/internal/run"
	"github.com/ddromanidis/arch-linter/internal/scaffold"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// version is overwritten at build time by the release process.
var version = "dev"

// Flags shared by every subcommand that has to find the architecture.
var (
	archPath     string
	configPath   string
	format       string
	baselinePath string
	buildTags    string
	preset       string
	importsOnly  bool
)

func main() {
	if err := root().Execute(); err != nil {
		// cobra has already printed the error.
		os.Exit(2)
	}
}

func root() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "archlint [packages]",
		Short: "Architecture rules for Go, including what your packages expose",
		Long: `archlint checks two things about every component you declare in arch.yaml.

  imports   what the component may depend on
  exports   what may appear in its public API

The second is the one other Go architecture linters do not have, and the reason this
exists: a repository may import its database driver and still have no business returning
it. No amount of import checking notices the difference.

Packages default to ./... . Rules come from arch.yaml, found by walking up from the
working directory; tool settings come from arch.config.yaml beside it, if present.

Exit status is 0 when clean or only warnings, 1 when something errored, and 2 when the
tool could not run at all — bad config, or code that does not compile.`,
		Args:          cobra.ArbitraryArgs,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(_ *cobra.Command, args []string) error {
			return lint(args)
		},
	}

	pf := cmd.PersistentFlags()
	pf.StringVar(&archPath, "arch", "", "path to arch.yaml (default: found by walking up)")
	pf.StringVar(&configPath, "config", "", "path to arch.config.yaml (default: beside arch.yaml)")
	pf.StringVar(&format, "format", "", "output format: text, json, github or sarif")
	pf.StringVar(&baselinePath, "baseline", "", "baseline file (overrides arch.config.yaml)")
	pf.StringVar(&buildTags, "tags", "", "comma-separated build tags, as `go build -tags`")
	cmd.Flags().BoolVar(&importsOnly, "imports-only", false,
		"skip type checking: import rules only, fast enough for a pre-commit hook")

	cmd.AddCommand(baselineCmd(), initCmd(), diagramCmd(), explainCmd())
	return cmd
}

func baselineCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "baseline [packages]",
		Short: "Freeze today's violations so a rule can be switched on before the code obeys it",
		Long: `Writes the current violations to a baseline file. Later runs forgive those and
fail only on new ones.

Counted per component, rule and package rather than per file and line, so the baseline
survives refactoring — move every offending function and nothing is reported — while a
tenth instance of something there were nine of still fails. Fix some, run this again, and
the numbers come down.`,
		Args:         cobra.ArbitraryArgs,
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return writeBaseline(args)
		},
	}
}

func initCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "init",
		Short:        "Write a starting arch.yaml",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return initArch()
		},
	}
	cmd.Flags().StringVar(&preset, "preset", "minimal",
		"one of: "+strings.Join(scaffold.Names(), ", "))
	return cmd
}

func diagramCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diagram",
		Short: "Print the component graph as Mermaid",
		Long: `Renders arch.yaml as a Mermaid flowchart.

Generated from the same file the linter enforces, so it cannot drift the way a diagram in
a wiki does. Dotted edges mark a dependency a component holds privately — imported, but
not re-exported.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return printDiagram()
		},
	}
}

func explainCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "explain <component> <package>",
		Short: "Say which rule decides whether a component may reach a package",
		Long: `Answers both questions for one pair, and names the rule that decided.

  archlint explain infra gorm.io/gorm

Debugging a config by deleting lines until the behaviour changes is what people do when a
tool will only say yes or no. The resolver already knows why; this makes it say so.`,
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return explain(args[0], args[1])
		},
	}
}

func explain(component, pkg string) error {
	rules, _, err := load()
	if err != nil {
		return err
	}
	res := rules.Resolver
	if !res.Known(component) {
		return fmt.Errorf("no component %q in %s", component, rules.ArchPath)
	}

	fmt.Printf("%s  →  %s\n", component, pkg)
	if owner := res.Component(pkg); owner != "" {
		fmt.Printf("  that package belongs to component %q\n", owner)
	} else if pkg != "" {
		fmt.Printf("  that package belongs to no component\n")
	}
	fmt.Println()

	// Widest claim decides the column, so it never wraps or staggers.
	width := len(component) + len(" may not expose")

	for _, q := range []struct {
		what   string
		reason config.Reason
		sev    config.Severity
	}{
		{"import", res.ExplainImport(component, pkg), rules.SeverityFor(component, analyzer.RuleImports)},
		{"expose", res.ExplainExport(component, pkg), rules.SeverityFor(component, analyzer.RuleExports)},
	} {
		verdict := "may not " + q.what
		if q.reason.Verdict == config.Allowed {
			verdict = "may " + q.what
		}
		// One column for the claim, one for the reason, so the two lines line up and the
		// difference between them is what you read rather than what you hunt for.
		claim := fmt.Sprintf("%s %s", component, verdict)
		fmt.Printf("  %-*s  %s\n", width, claim, describeReason(q.reason))
		if q.reason.Verdict != config.Allowed {
			fmt.Printf("  %-*s  reported as: %s\n", width, "", q.sev)
		}
	}
	return nil
}

func describeReason(r config.Reason) string {
	switch r.Rule {
	case config.ReasonSameComponent, config.ReasonUnknown, config.ReasonNotOnAnyList:
		return "(" + r.Rule + ")"
	case config.ReasonUnconstrained:
		return "(" + r.Rule + " — " + r.Source + " is absent)"
	}
	return "matched `" + r.Rule + "` in " + r.Source
}

// load finds arch.yaml and reads both config files.
func load() (*analyzer.Rules, string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, "", err
	}
	path := archPath
	if path == "" {
		found, ok := findUp(wd, "arch.yaml")
		if !ok {
			return nil, wd, fmt.Errorf(
				"no arch.yaml here or above. `archlint init` writes a starting point")
		}
		path = found
	}
	rules, err := analyzer.LoadFrom(path, configPath, wd)
	if err != nil {
		return nil, wd, err
	}
	if format != "" {
		rules.Config.Output.Format = format
	}
	return rules, wd, nil
}

// analyse runs both the whole-config checks and the per-package ones.
func analyse(rules *analyzer.Rules, wd string, args []string) ([]report.Finding, error) {
	patterns := args
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	// Whether a component matching nothing is a mistake or just a narrow run.
	whole := slices.Contains(patterns, "./...")

	// Cycles first: they are a property of the architecture rather than of the code, so
	// they are reported even when the packages fail to load — which is exactly when you
	// most want to know the architecture itself is sound.
	findings := run.Cycles(rules)

	analysed, err := run.Analyse(wd, patterns, rules, whole, splitTags(buildTags), importsOnly)
	if err != nil {
		if len(findings) > 0 {
			// Say what was learned before saying what could not be.
			_ = report.Write(os.Stderr, "text", findings, filepath.Dir(rules.ArchPath), false)
		}
		return nil, err
	}
	return append(findings, analysed...), nil
}

func splitTags(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func lint(args []string) error {
	rules, wd, err := load()
	if err != nil {
		return err
	}
	findings, err := analyse(rules, wd, args)
	if err != nil {
		return err
	}
	findings = dropDisabled(findings)

	root := filepath.Dir(rules.ArchPath)
	blPath := resolveBaseline(rules, root)

	var forgiven int
	var stale []baseline.Entry
	if blPath != "" {
		bl, err := baseline.Load(blPath)
		if err != nil {
			return err
		}
		stale = bl.Stale(findings)
		findings, forgiven = bl.Apply(findings)
	}

	if err := report.Write(os.Stdout, rules.Config.Output.Format, findings, root,
		useColour(rules.Config.Output.Color)); err != nil {
		return err
	}

	// Only for a human. Extra lines in json or sarif output would corrupt it.
	if rules.Config.Output.Format == "text" {
		if forgiven > 0 {
			fmt.Printf("%d violation(s) forgiven by the baseline\n", forgiven)
		}
		if len(stale) > 0 {
			fmt.Printf("%d baseline entr(y/ies) now over-count: run `archlint baseline` "+
				"to lock the improvement in\n", len(stale))
		}
	}

	// Warnings are reported and forgiven. That is the point of the setting: it lets a team
	// switch a rule on before they have finished obeying it, which is the only way a rule
	// ever gets switched on at all.
	for _, f := range findings {
		if f.Severity == string(config.Error) {
			os.Exit(1)
		}
	}
	return nil
}

func writeBaseline(args []string) error {
	rules, wd, err := load()
	if err != nil {
		return err
	}
	findings, err := analyse(rules, wd, args)
	if err != nil {
		return err
	}
	findings = dropDisabled(findings)

	root := filepath.Dir(rules.ArchPath)
	blPath := resolveBaseline(rules, root)
	if blPath == "" {
		blPath = filepath.Join(root, ".archlint-baseline.yaml")
	}
	if err := baseline.Save(blPath, findings); err != nil {
		return err
	}
	shown, _ := filepath.Rel(wd, blPath)
	fmt.Printf("froze %d violation(s) into %s\n", len(findings), shown)
	return nil
}

func initArch() error {
	path := archPath
	if path == "" {
		path = "arch.yaml"
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	// Best effort: a module path makes the file immediately usable, and its absence only
	// means archlint reads go.mod itself later.
	var module string
	if goMod, ok := findUp(wd, "go.mod"); ok {
		module, _ = config.ModulePath(goMod)
	}
	if err := scaffold.Write(path, preset, module); err != nil {
		return err
	}
	fmt.Printf("wrote %s (%s preset)\n", path, preset)
	fmt.Println("edit it to match your components, then run `archlint`")
	return nil
}

func printDiagram() error {
	path := archPath
	if path == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		found, ok := findUp(wd, "arch.yaml")
		if !ok {
			return fmt.Errorf("no arch.yaml here or above")
		}
		path = found
	}
	arch, err := config.ParseArch(path)
	if err != nil {
		return err
	}
	return diagram.Mermaid(os.Stdout, arch)
}

// dropDisabled removes findings whose rule is switched off. Done here rather than at the
// point of detection so a baseline can still see what a rule *would* have said.
func dropDisabled(findings []report.Finding) []report.Finding {
	kept := findings[:0]
	for _, f := range findings {
		if f.Severity != string(config.Off) {
			kept = append(kept, f)
		}
	}
	return kept
}

// resolveBaseline picks the baseline path from the flag, then the config, then nowhere.
func resolveBaseline(rules *analyzer.Rules, root string) string {
	p := baselinePath
	if p == "" {
		p = rules.Config.Baseline
	}
	if p != "" && !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	return p
}

func useColour(mode string) bool {
	switch mode {
	case "always":
		return true
	case "never":
		return false
	}
	// Auto. Colour is for a person reading a terminal; a pipe or a CI log is neither, and
	// escape codes in a log file are worse than no colour at all.
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

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
