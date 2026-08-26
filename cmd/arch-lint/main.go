// Command arch-lint checks that a Go module obeys the architecture written in its
// arch.yaml — both what each component may import, and what each component may expose.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ddromanidis/arch-linter/analyzer"
	"github.com/ddromanidis/arch-linter/config"
	"github.com/ddromanidis/arch-linter/internal/baseline"
	"github.com/ddromanidis/arch-linter/internal/diagram"
	"github.com/ddromanidis/arch-linter/internal/report"
	"github.com/ddromanidis/arch-linter/internal/run"
	"github.com/ddromanidis/arch-linter/internal/scaffold"
	"golang.org/x/term"
)

const usage = `arch-lint — architecture rules for Go, including what your packages expose

usage:
  arch-lint [flags] [packages]          check the architecture
  arch-lint baseline [flags] [packages] freeze today's violations and exit
  arch-lint init [-preset NAME]         write a starting arch.yaml
  arch-lint diagram [-arch PATH]        print the component graph as Mermaid

Packages default to ./... . Rules come from arch.yaml, found by walking up from the
working directory; tool settings come from arch.config.yaml beside it, if present.

A baseline forgives violations that already existed, so a rule can be switched on
before the code obeys it. It counts violations per component, rule and package rather
than per line, so it survives refactoring but still fails when a count goes up.

flags:
  -arch PATH      arch.yaml to use (default: found by walking up)
  -config PATH    arch.config.yaml to use (default: beside arch.yaml)
  -format FORMAT  text, json, github or sarif (overrides arch.config.yaml)
  -baseline PATH  baseline file (overrides arch.config.yaml)
  -preset NAME    init only: minimal, layered, hexagonal or ddd
  -version        print the version and exit

exit status:
  0  no violations, or only warnings
  1  at least one error-severity violation
  2  the tool could not run — bad config, or code that does not compile
`

// version is overwritten at build time by the release process.
var version = "dev"

func main() {
	// The subcommand is read before flag parsing so that `arch-lint baseline -format json`
	// works the way people expect rather than being rejected as a stray argument.
	args := os.Args[1:]
	command := "lint"
	if len(args) > 0 {
		switch args[0] {
		case "baseline", "init", "diagram":
			command, args = args[0], args[1:]
		}
	}

	fs := flag.NewFlagSet("arch-lint", flag.ExitOnError)
	var (
		archPath     = fs.String("arch", "", "path to arch.yaml")
		configPath   = fs.String("config", "", "path to arch.config.yaml")
		format       = fs.String("format", "", "output format")
		baselinePath = fs.String("baseline", "", "path to the baseline file")
		preset       = fs.String("preset", "minimal", "init only: "+strings.Join(scaffold.Names(), ", "))
		showVer      = fs.Bool("version", false, "print version")
	)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	_ = fs.Parse(args)

	if *showVer {
		fmt.Println("arch-lint", version)
		return
	}

	opts := options{
		arch:     *archPath,
		config:   *configPath,
		format:   *format,
		baseline: *baselinePath,
		preset:   *preset,
		patterns: fs.Args(),
		write:    command == "baseline",
	}

	var err error
	switch command {
	case "init":
		err = initArch(opts)
	case "diagram":
		err = diagramArch(opts)
	default:
		err = lint(opts)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "arch-lint:", err)
		os.Exit(2)
	}
}

type options struct {
	arch, config, format, baseline, preset string
	patterns                               []string
	write                                  bool
}

// initArch writes a starting arch.yaml, seeded from go.mod so the first run works.
func initArch(o options) error {
	path := o.arch
	if path == "" {
		path = "arch.yaml"
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	// Best effort: a module path makes the file immediately usable, and its absence only
	// means arch-lint reads go.mod itself later.
	var module string
	if goMod, ok := findUp(wd, "go.mod"); ok {
		module, _ = config.ModulePath(goMod)
	}
	if err := scaffold.Write(path, o.preset, module); err != nil {
		return err
	}
	fmt.Printf("wrote %s (%s preset)\n", path, o.preset)
	fmt.Println("edit it to match your components, then run `arch-lint`")
	return nil
}

// diagramArch prints the component graph, for pasting into a README that then cannot drift
// from what the build enforces.
func diagramArch(o options) error {
	path := o.arch
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

func lint(o options) error {
	patterns := o.patterns
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	archPath, configPath, format := o.arch, o.config, o.format

	wd, err := os.Getwd()
	if err != nil {
		return err
	}

	if archPath == "" {
		found, ok := findUp(wd, "arch.yaml")
		if !ok {
			return fmt.Errorf(
				"no arch.yaml here or above. `arch-lint init` writes a starting point")
		}
		archPath = found
	}

	rules, err := analyzer.Load(archPath, configPath)
	if err != nil {
		return err
	}
	if format != "" {
		rules.Config.Output.Format = format
	}

	findings, err := run.Analyse(wd, patterns, rules)
	if err != nil {
		return err
	}

	// Anything switched off is dropped here rather than at the point of detection, so that
	// a future baseline can still see what a rule *would* have said.
	kept := findings[:0]
	for _, f := range findings {
		if f.Severity != string(config.Off) {
			kept = append(kept, f)
		}
	}
	findings = kept

	root := filepath.Dir(archPath)

	// The baseline path may come from the flag, the config, or nowhere.
	blPath := o.baseline
	if blPath == "" && rules.Config.Baseline != "" {
		blPath = rules.Config.Baseline
	}
	if blPath != "" && !filepath.IsAbs(blPath) {
		blPath = filepath.Join(root, blPath)
	}

	if o.write {
		if blPath == "" {
			blPath = filepath.Join(root, ".arch-baseline.yaml")
		}
		if err := baseline.Save(blPath, findings); err != nil {
			return err
		}
		rel, _ := filepath.Rel(wd, blPath)
		fmt.Printf("froze %d violation(s) into %s\n", len(findings), rel)
		return nil
	}

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

	// Only for a human. Adding lines to json or sarif output would corrupt it.
	if rules.Config.Output.Format == "text" {
		if forgiven > 0 {
			fmt.Printf("%d violation(s) forgiven by the baseline\n", forgiven)
		}
		if len(stale) > 0 {
			fmt.Printf("%d baseline entr(y/ies) now over-count: re-run `arch-lint baseline` "+
				"to lock the improvement in\n", len(stale))
		}
	}

	// Warnings are reported and forgiven. That is the whole point of the setting: it lets a
	// team switch a rule on before they have finished obeying it, which is the only way a
	// rule ever gets switched on at all.
	for _, f := range findings {
		if f.Severity == string(config.Error) {
			os.Exit(1)
		}
	}
	return nil
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
