// Package run drives the analyzer over a set of packages and collects findings.
//
// A driver of its own rather than analysis/singlechecker, because the CLI wants things the
// analysis framework deliberately does not offer: a choice of output format, an exit code
// that distinguishes errors from warnings, and eventually a baseline. `go vet -vettool`
// still gets singlechecker; this is the richer path.
package run

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ddromanidis/arch-linter/analyzer"
	"github.com/ddromanidis/arch-linter/config"
	"github.com/ddromanidis/arch-linter/internal/cache"
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

// Cycles reports dependency loops in the declared component graph.
//
// Separate from Analyse and run once, because a cycle is a property of arch.yaml rather
// than of any package — reporting it from the per-package analyzer would repeat the same
// loop once for every package in the module. It also needs no code and no build, so it
// answers instantly and works on a repository that does not compile.
func Cycles(rules *analyzer.Rules) []report.Finding {
	if rules.Config.Severity.Cycles == config.Off || rules.Arch == nil {
		return nil
	}
	var out []report.Finding
	for _, cycle := range rules.Arch.Cycles() {
		// Anchored at the first component in the loop, which is the alphabetically
		// smallest, so the anchor is stable rather than an artefact of walk order.
		head := cycle[0]
		out = append(out, report.Finding{
			Rule:      analyzer.RuleCycles,
			Component: head,
			Target:    cycle.String(),
			File:      rules.ArchPath,
			Line:      rules.Arch.ComponentLine(head),
			Column:    1,
			Message:   "dependency cycle: " + cycle.String(),
			Severity:  string(rules.Config.Severity.Cycles),
		})
	}
	return out
}

// Coverage reports components that matched nothing and packages nothing claimed.
//
// Both are silent failures of the same kind, and the kind this tool exists to prevent: a
// component whose path is misspelled has rules that can never fire, and a package no
// component claims has no rules at all. Neither is distinguishable from "enforced and
// clean" by looking at the output, which is the worst property a linter can have.
//
// wholeModule guards the first check. Running `archlint ./internal/app` legitimately
// matches only one component, and reporting the other twelve as dead would make targeted
// runs useless.
func coverage(
	rules *analyzer.Rules,
	matched map[string]bool,
	unclassified []string,
	wholeModule bool,
) []report.Finding {
	var out []report.Finding
	cfg := rules.Config

	if wholeModule && cfg.Severity.Coverage != config.Off && rules.Arch != nil {
		for _, name := range rules.Arch.ComponentNames() {
			if matched[name] {
				continue
			}
			out = append(out, report.Finding{
				Rule:      analyzer.RuleCoverage,
				Component: name,
				Target:    name,
				File:      rules.ArchPath,
				Line:      rules.Arch.ComponentLine(name),
				Column:    1,
				Message: "component " + name + " matched no packages — its rules never ran" +
					" (check the path)",
				Severity: string(rules.SeverityFor(name, analyzer.RuleCoverage)),
			})
		}
	}

	if cfg.Severity.Unclassified != config.Off {
		for _, pkg := range unclassified {
			out = append(out, report.Finding{
				Rule:     analyzer.RuleUnclassified,
				Target:   pkg,
				File:     rules.ArchPath,
				Line:     1,
				Column:   1,
				Message:  pkg + " belongs to no component, so no rule applies to it",
				Severity: string(cfg.Severity.Unclassified),
			})
		}
	}
	return out
}

// Analyse loads patterns and returns every violation.
//
// wholeModule says whether the patterns cover the entire module, which decides whether a
// component matching nothing is evidence of a mistake or just of a narrow run.
func Analyse(
	dir string,
	patterns []string,
	rules *analyzer.Rules,
	wholeModule bool,
	tags []string,
	importsOnly bool,
	c *cache.Cache,
) ([]report.Finding, error) {
	// Import rules need no types at all: a file's import block is the whole answer, and
	// syntax gives it. Skipping the type checker is the difference between ~1.6s and
	// ~100ms on a 160-package module, which is the difference between a pre-commit hook
	// people keep and one they bypass. Export rules are dropped in this mode because they
	// cannot be answered without resolving types — the point of the whole tool — so CI
	// still has to run the full analysis.
	mode := Mode
	if importsOnly {
		mode = packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedModule
	}
	cfg := &packages.Config{Dir: dir, Mode: mode, Tests: rules.Config.IncludeTests}
	// Build tags. Without these, a repository built two ways — a control plane and a
	// tenant, say — has one of them silently unanalysed, and the half you did not ask for
	// looks exactly like a half with no violations.
	if len(tags) > 0 {
		cfg.BuildFlags = []string{"-tags=" + strings.Join(tags, ",")}
	}
	// Two phases when caching, one when not.
	//
	// The cost of a run is the type checking, not the analysis, so a cache that only skips
	// the analysis saves nothing — which is exactly what a first attempt at this did. The
	// load itself has to be skipped, and that means learning the package graph cheaply
	// first, deciding what changed, and then type-checking only that.
	// Without a cache there is nothing to decide, so the first load is the only load and
	// must carry types. Loading the cheap graph in that case produced packages with no
	// syntax, every one of which was then skipped — a run that analysed nothing and said
	// so by reporting nothing.
	phase := "full"
	if c != nil {
		phase = "graph"
	}
	graph, err := load(cfg, patterns, phase)
	if err != nil {
		return nil, err
	}
	if err := loadErrors(graph, importsOnly); err != nil {
		return nil, err
	}

	a := analyzer.New(rules)
	var findings []report.Finding
	matched := map[string]bool{}
	var unclassified []string

	// Fingerprints cover the import graph, so a package whose dependency changed is
	// invalidated even though its own files did not move.
	var prints map[string]string
	if c != nil {
		prints = cache.Fingerprints(graphInputs(graph))
	}

	// Decide what still needs type checking.
	var stale []string
	cached := map[string][]report.Finding{}
	for _, pkg := range graph {
		component := rules.Resolver.Component(pkg.PkgPath)
		classify(rules, pkg.PkgPath, component, matched, &unclassified)
		if component == "" || rules.Excluded(pkg.PkgPath) || len(pkg.CompiledGoFiles) == 0 {
			continue
		}
		if c != nil {
			if hit, ok := c.Lookup(pkg.PkgPath, prints[pkg.PkgPath]); ok {
				cached[pkg.PkgPath] = hit
				continue
			}
		}
		stale = append(stale, pkg.PkgPath)
	}
	for _, f := range cached {
		findings = append(findings, f...)
	}

	// Phase two: full types, for the packages that need them.
	var typed []*packages.Package
	switch {
	case c == nil:
		typed = graph // the single-phase path already loaded with full types
	case len(stale) > 0:
		if typed, err = load(cfg, stale, "full"); err != nil {
			return nil, err
		}
		if err := loadErrors(typed, importsOnly); err != nil {
			return nil, err
		}
	}

	for _, pkg := range typed {
		if len(pkg.Syntax) == 0 {
			continue
		}
		component := rules.Resolver.Component(pkg.PkgPath)
		if component == "" || rules.Excluded(pkg.PkgPath) {
			continue
		}
		if importsOnly {
			findings = append(findings, importsOnlyFindings(pkg, rules, component)...)
			continue
		}
		produced, err := analysePackage(a, pkg, rules, component)
		if err != nil {
			return nil, err
		}
		findings = append(findings, produced...)
		if c != nil {
			// Stored even when empty: "this package is clean" is exactly the answer worth
			// not recomputing.
			c.Store(pkg.PkgPath, prints[pkg.PkgPath], produced)
		}
	}

	sort.Strings(unclassified)
	findings = append(findings, coverage(rules, matched, unclassified, wholeModule)...)
	return findings, nil
}

// importsOnlyFindings applies the import rule from syntax alone.
func importsOnlyFindings(
	pkg *packages.Package,
	rules *analyzer.Rules,
	component string,
) []report.Finding {
	var out []report.Finding
	for _, f := range pkg.Syntax {
		for _, spec := range f.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				continue
			}
			verdict := rules.Resolver.CheckImport(component, path)
			if verdict == config.Allowed {
				continue
			}
			pos := pkg.Fset.Position(spec.Pos())
			if rules.Excluded(pkg.PkgPath) {
				continue
			}
			suffix := ""
			if verdict == config.Denied {
				suffix = " (denied)"
			}
			out = append(out, report.Finding{
				Rule:      analyzer.RuleImports,
				Component: component,
				Target:    path,
				File:      pos.Filename,
				Line:      pos.Line,
				Column:    pos.Column,
				Message: component + " may not import " +
					rules.Resolver.Describe(path) + suffix,
				Severity: string(rules.SeverityFor(component, analyzer.RuleImports)),
			})
		}
	}
	return out
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
	case analyzer.RuleCycles:
		return cfg.Severity.Cycles
	}
	return cfg.Severity.Exports
}

// GraphMode is enough to learn what packages exist, what files they are made of and what
// they import — without type checking, which is the expensive part.
const GraphMode = packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
	packages.NeedImports | packages.NeedModule

// load runs the go tool over patterns. kind "full" asks for type information; anything
// else asks only for the graph.
func load(base *packages.Config, patterns []string, kind string) ([]*packages.Package, error) {
	cfg := *base
	if kind != "full" && base.Mode == Mode {
		cfg.Mode = GraphMode
	}
	pkgs, err := packages.Load(&cfg, patterns...)
	if err != nil {
		return nil, fmt.Errorf("loading packages: %w", err)
	}
	return pkgs, nil
}

// loadErrors refuses to analyse code that does not type-check.
//
// Deciding whether a signature leaks means resolving what its types are, and a
// half-resolved answer is exactly the quietly-wrong result this tool exists to avoid.
func loadErrors(pkgs []*packages.Package, importsOnly bool) error {
	if importsOnly {
		// Without type information a type error is neither detectable nor relevant: the
		// import block parses regardless, which is part of why this mode is useful on a
		// tree that does not build yet.
		return nil
	}
	var msgs []string
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			msgs = append(msgs, e.Error())
		}
	})
	if len(msgs) == 0 {
		return nil
	}
	return fmt.Errorf("the packages do not type-check, so the architecture cannot be "+
		"verified:\n  %s", strings.Join(msgs, "\n  "))
}

// graphInputs adapts loaded packages to what fingerprinting needs.
func graphInputs(pkgs []*packages.Package) []cache.Package {
	out := make([]cache.Package, 0, len(pkgs))
	for _, pkg := range pkgs {
		deps := make([]string, 0, len(pkg.Imports))
		for dep := range pkg.Imports {
			deps = append(deps, dep)
		}
		out = append(out, cache.Package{
			ImportPath: pkg.PkgPath,
			Files:      pkg.CompiledGoFiles,
			Imports:    deps,
		})
	}
	return out
}

// classify records which components were seen and which packages nothing claimed.
func classify(
	rules *analyzer.Rules,
	pkgPath, component string,
	matched map[string]bool,
	unclassified *[]string,
) {
	switch {
	case component != "":
		matched[component] = true
	case !rules.Excluded(pkgPath):
		// Only packages inside this module. Dependencies are nobody's component and
		// reporting them would be absurd.
		if mod := rules.Resolver.Module(); mod != "" &&
			(pkgPath == mod || strings.HasPrefix(pkgPath, mod+"/")) {
			*unclassified = append(*unclassified, pkgPath)
		}
	}
}

// analysePackage runs the analyzer over one package and returns its findings.
func analysePackage(
	a *analysis.Analyzer,
	pkg *packages.Package,
	rules *analyzer.Rules,
	component string,
) ([]report.Finding, error) {
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

	out := make([]report.Finding, 0, len(violations))
	for _, v := range violations {
		pos := pkg.Fset.Position(v.Pos)
		out = append(out, report.Finding{
			Rule:      v.Rule,
			Component: cmp(v.Component, component),
			Target:    v.Target,
			File:      pos.Filename,
			Line:      pos.Line,
			Column:    pos.Column,
			Message:   v.Message,
			Severity:  string(rules.SeverityFor(cmp(v.Component, component), v.Rule)),
		})
	}
	return out, nil
}
