// Package run drives the analyzer over a set of packages and collects findings.
//
// A driver of its own rather than analysis/singlechecker, because the CLI wants things the
// analysis framework deliberately does not offer: a choice of output format, an exit code
// that distinguishes errors from warnings, and eventually a baseline. `go vet -vettool`
// still gets singlechecker; this is the richer path.
package run

import (
	"fmt"
	"strings"

	"github.com/ddromanidis/arch-linter/analyzer"
	"github.com/ddromanidis/arch-linter/config"
	"github.com/ddromanidis/arch-linter/internal/report"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"
)

// Mode is everything the analyzer needs from a loaded package.
const Mode = packages.NeedName |
	packages.NeedFiles |
	packages.NeedSyntax |
	packages.NeedTypes |
	packages.NeedTypesInfo |
	packages.NeedImports |
	packages.NeedDeps |
	packages.NeedModule

// Analyse loads patterns and returns every violation.
func Analyse(dir string, patterns []string, rules *analyzer.Rules) ([]report.Finding, error) {
	cfg := &packages.Config{Dir: dir, Mode: Mode, Tests: rules.Config.IncludeTests}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, fmt.Errorf("loading packages: %w", err)
	}

	// Type errors mean the type-checked answers would be wrong, and a leak check on
	// half-resolved types is exactly the kind of quietly-wrong result this tool exists to
	// avoid. Say so rather than reporting against them.
	var loadErrs []string
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			loadErrs = append(loadErrs, e.Error())
		}
	})
	if len(loadErrs) > 0 {
		return nil, fmt.Errorf("the packages do not type-check, so the architecture cannot be "+
			"verified:\n  %s", strings.Join(loadErrs, "\n  "))
	}

	a := analyzer.New(rules)
	var findings []report.Finding

	for _, pkg := range pkgs {
		if len(pkg.Syntax) == 0 {
			continue
		}
		component := rules.Resolver.Component(pkg.PkgPath)
		pass := &analysis.Pass{
			Analyzer:   a,
			Fset:       pkg.Fset,
			Files:      pkg.Syntax,
			Pkg:        pkg.Types,
			TypesInfo:  pkg.TypesInfo,
			TypesSizes: pkg.TypesSizes,
			ResultOf:   map[*analysis.Analyzer]any{},
			Report: func(d analysis.Diagnostic) {
				pos := pkg.Fset.Position(d.Pos)
				sev := severityOf(d.Category, rules.Config)
				findings = append(findings, report.Finding{
					Rule:      d.Category,
					Component: component,
					File:      pos.Filename,
					Line:      pos.Line,
					Column:    pos.Column,
					Message:   d.Message,
					Severity:  string(sev),
				})
			},
		}
		if _, err := a.Run(pass); err != nil {
			return nil, fmt.Errorf("%s: %w", pkg.PkgPath, err)
		}
	}
	return findings, nil
}

// severityOf maps a diagnostic's category to the configured severity. The category is set
// where the diagnostic is created, so this cannot drift from the message text.
func severityOf(category string, cfg *config.Config) config.Severity {
	if category == analyzer.RuleImports {
		return cfg.Severity.Imports
	}
	return cfg.Severity.Exports
}
