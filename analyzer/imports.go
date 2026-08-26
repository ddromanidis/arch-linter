package analyzer

import (
	"strconv"

	"github.com/ddromanidis/arch-linter/config"

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
			verdict := rules.Resolver.CheckImport(component, path)
			if verdict == config.Allowed {
				continue
			}
			c.target = path
			c.report(analysis.Diagnostic{
				Category: RuleImports,
				Pos:      spec.Pos(),
				End:      spec.End(),
				Message: component + " may not import " +
					rules.Resolver.Describe(path) + describePath(rules, path) +
					because(verdict),
			})
		}
	}
}

// because names the rule that produced a verdict.
//
// A banned import and an undeclared one need opposite fixes — delete the line of Go, or
// add a line to arch.yaml — and a message that did not distinguish them would send people
// to the wrong file.
func because(v config.Verdict) string {
	if v == config.Denied {
		return " (denied)"
	}
	return ""
}

// describePath adds the import path in parentheses when Describe returned a component name,
// so a message names both the rule you broke and the thing you actually wrote.
func describePath(rules *Rules, path string) string {
	if rules.Resolver.Component(path) == "" {
		return ""
	}
	return " (" + path + ")"
}
