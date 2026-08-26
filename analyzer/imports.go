package analyzer

import (
	"strconv"

	"golang.org/x/tools/go/analysis"
)

// checkImports enforces the import allowlist.
//
// Reported against the import spec rather than the package, so the diagnostic lands on the
// line you have to delete. Walking the syntax rather than pass.Pkg.Imports() costs nothing
// and is the only way to get that position.
func (c *checker) checkImports() {
	pass, rules, component := c.pass, c.rules, c.component

	for _, f := range pass.Files {
		if inSkippedFile(pass, rules, f.Pos()) {
			continue
		}
		for _, spec := range f.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				continue
			}
			if rules.Resolver.AllowsImport(component, path) {
				continue
			}
			c.target = path
			c.report(analysis.Diagnostic{
				Category: RuleImports,
				Pos:      spec.Pos(),
				End:      spec.End(),
				Message: component + " may not import " +
					rules.Resolver.Describe(path) + describePath(rules, path),
			})
		}
	}
}

// describePath adds the import path in parentheses when Describe returned a component name,
// so a message names both the rule you broke and the thing you actually wrote.
func describePath(rules *Rules, path string) string {
	if rules.Resolver.Component(path) == "" {
		return ""
	}
	return " (" + path + ")"
}
