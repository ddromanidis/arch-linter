package analyzer

import (
	"go/types"
	"sort"

	"golang.org/x/tools/go/analysis"
)

// checkExports enforces the export allowlist: what a component may expose, as opposed to
// what it may use.
//
// The surface is everything reachable from outside — exported package-scope declarations,
// and the exported methods of exported types, which live on the type rather than in the
// package scope and so have to be collected separately.
func checkExports(pass *analysis.Pass, rules *Rules, component string) {
	scope := pass.Pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() || inSkippedFile(pass, rules, obj.Pos()) {
			continue
		}
		report(pass, rules, component, obj, obj.Type(), name)

		named, ok := obj.Type().(*types.Named)
		if !ok {
			continue
		}
		// Only methods this package declares. An exported var or const can have a type
		// from elsewhere — `const Mode = packages.NeedName|...` is a packages.LoadMode —
		// and that type's methods belong to that package, not to us. Walking them
		// attributes somebody else's code to this component and, worse, reports a
		// diagnostic at a position inside the module cache, which nobody can act on.
		if named.Obj() == nil || named.Obj().Pkg() != pass.Pkg {
			continue
		}
		for i := range named.NumMethods() {
			m := named.Method(i)
			if !m.Exported() || inSkippedFile(pass, rules, m.Pos()) {
				continue
			}
			report(pass, rules, component, m, m.Type(), name+"."+m.Name())
		}
	}
}

// report flags every disallowed package a type exposes.
func report(
	pass *analysis.Pass,
	rules *Rules,
	component string,
	obj types.Object,
	typ types.Type,
	label string,
) {
	var bad []string
	for pkg := range referencedPackages(pass.Pkg, typ) {
		if pkg == pass.Pkg {
			continue
		}
		if rules.Resolver.AllowsExport(component, pkg.Path()) {
			continue
		}
		bad = append(bad, pkg.Path())
	}
	if len(bad) == 0 {
		return
	}
	// Deterministic: map iteration order must not decide what a diagnostic says, or the
	// same code produces different output on consecutive runs.
	sort.Strings(bad)

	for _, path := range bad {
		pass.Report(analysis.Diagnostic{
			Category: RuleExports,
			Pos:      obj.Pos(),
			Message: label + " exposes " + rules.Resolver.Describe(path) +
				describePath(rules, path) + ", which " + component + " may not export",
		})
	}
}
