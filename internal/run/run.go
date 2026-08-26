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
			// Discarded on purpose. Diagnostics exist for go vet and golangci-lint, which
			// understand nothing else; this driver reads the structured result instead, so
			// that a baseline can be keyed on the package at fault rather than on the
			// wording of a sentence.
			Report: func(analysis.Diagnostic) {},
		}
		res, err := a.Run(pass)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", pkg.PkgPath, err)
		}
		violations, _ := res.([]analyzer.Violation)
		for _, v := range violations {
			pos := pkg.Fset.Position(v.Pos)
			findings = append(findings, report.Finding{
				Rule:      v.Rule,
				Component: cmp(v.Component, component),
				Target:    v.Target,
				File:      pos.Filename,
				Line:      pos.Line,
				Column:    pos.Column,
				Message:   v.Message,
				Severity:  string(severityOf(v.Rule, rules.Config)),
			})
		}
	}
	return findings, nil
}

// cmp prefers a non-empty value, so a violation that knows its own component wins.
func cmp(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// severityOf maps a diagnostic's category to the configured severity. The category is set
// where the diagnostic is created, so this cannot drift from the message text.
func severityOf(category string, cfg *config.Config) config.Severity {
	switch category {
	case analyzer.RuleImports:
		return cfg.Severity.Imports
	case analyzer.RuleWaivers:
		return cfg.Severity.Waivers
	}
	return cfg.Severity.Exports
}
