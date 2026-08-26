package analyzer

import (
	"go/types"

	"github.com/ddromanidis/arch-linter/config"
	"sort"

	"golang.org/x/tools/go/analysis"
)

// checkExports enforces the export allowlist: what a component may expose, as opposed to
// what it may use.
//
// The surface is everything reachable from outside — exported package-scope declarations,
// and the exported methods of exported types, which live on the type rather than in the
// package scope and so have to be collected separately.
func (c *checker) checkExports() {
	pass, rules := c.pass, c.rules

	scope := pass.Pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() || inSkippedFile(pass, rules, obj.Pos()) {
			continue
		}
		c.leaks(obj, obj.Type(), name)

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
			c.leaks(m, m.Type(), name+"."+m.Name())
		}
	}
}

// leaks flags every disallowed package a type exposes.
func (c *checker) leaks(obj types.Object, typ types.Type, label string) {
	pass, rules, component := c.pass, c.rules, c.component

	bad := map[string]config.Verdict{}
	for pkg := range referencedPackages(pass.Pkg, typ) {
		if pkg == pass.Pkg {
			continue
		}
		if v := rules.Resolver.CheckExport(component, pkg.Path()); v != config.Allowed {
			bad[pkg.Path()] = v
		}
	}
	if len(bad) == 0 {
		return
	}
	// Deterministic: map iteration order must not decide what a diagnostic says, or the
	// same code produces different output on consecutive runs.
	paths := make([]string, 0, len(bad))
	for p := range bad {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, path := range paths {
		c.target = path
		c.report(analysis.Diagnostic{
			Category: RuleExports,
			Pos:      obj.Pos(),
			Message: label + " exposes " + rules.Resolver.Describe(path) +
				describePath(rules, path) + ", which " + component + " may not export" +
				because(bad[path]),
		})
	}
}
