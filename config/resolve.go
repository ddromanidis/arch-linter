package config

import (
	"sort"
	"strings"
)

// Resolver answers the two questions every rule needs: which component does this package
// belong to, and may that component reach this target.
//
// Built once from an Arch and then read-only, so the analyzer can share one across every
// package it visits without locking.
type Resolver struct {
	module string
	// owners is sorted longest-prefix-first, so the most specific pattern wins.
	owners   []owner
	rules    map[string]componentRules
	defaults componentRules
}

type owner struct {
	prefix    string // fully qualified import path
	recursive bool
	component string
}

type componentRules struct {
	imports matcher
	exports matcher
}

// matcher decides whether a target — a component name, or an import path — is permitted.
type matcher struct {
	names    map[string]bool // component names
	exact    map[string]bool // exact import paths
	prefixes []string        // import path prefixes from a trailing /...
}

func (m matcher) allows(component, importPath string) bool {
	if component != "" && m.names[component] {
		return true
	}
	if m.exact[importPath] {
		return true
	}
	for _, p := range m.prefixes {
		if importPath == p || strings.HasPrefix(importPath, p+"/") {
			return true
		}
	}
	return false
}

// NewResolver compiles an Arch into something that can answer questions quickly.
//
// module overrides Arch.Module when non-empty, which is how the go.mod value reaches here.
func NewResolver(a *Arch, module string) *Resolver {
	if module == "" {
		module = a.Module
	}
	r := &Resolver{
		module: module,
		rules:  make(map[string]componentRules, len(a.Components)),
	}

	for name, c := range a.Components {
		for _, p := range c.patterns() {
			recursive := strings.HasSuffix(p, "/...")
			r.owners = append(r.owners, owner{
				prefix:    qualify(module, strings.TrimSuffix(p, "/...")),
				recursive: recursive,
				component: name,
			})
		}
		r.rules[name] = componentRules{
			imports: r.compile(a, c.Imports),
			exports: r.compile(a, c.Exports),
		}
	}
	r.defaults = componentRules{
		imports: r.compile(a, a.Defaults.Imports),
		exports: r.compile(a, a.Defaults.Exports),
	}

	// Longest prefix first. With `internal/...` and `internal/domain/...` both declared,
	// a package under internal/domain belongs to the more specific one — anything else
	// would make a nested component impossible to express.
	sort.SliceStable(r.owners, func(i, j int) bool {
		return len(r.owners[i].prefix) > len(r.owners[j].prefix)
	})
	return r
}

func (r *Resolver) compile(a *Arch, rules []string) matcher {
	m := matcher{names: map[string]bool{}, exact: map[string]bool{}}
	for _, rule := range rules {
		if _, ok := a.Components[rule]; ok {
			m.names[rule] = true
			continue
		}
		if base, found := strings.CutSuffix(rule, "/..."); found {
			m.prefixes = append(m.prefixes, qualifyRule(r.module, base))
			continue
		}
		m.exact[qualifyRule(r.module, rule)] = true
	}
	return m
}

// Component returns the component owning an import path, or "" if the path is outside the
// architecture — a third-party module, the standard library, or a corner of the repo
// nobody has classified yet.
func (r *Resolver) Component(importPath string) string {
	for _, o := range r.owners {
		if importPath == o.prefix {
			return o.component
		}
		if o.recursive && strings.HasPrefix(importPath, o.prefix+"/") {
			return o.component
		}
	}
	return ""
}

// Known reports whether a component is declared.
func (r *Resolver) Known(component string) bool {
	_, ok := r.rules[component]
	return ok
}

// Module is the Go module path the architecture is written against.
func (r *Resolver) Module() string { return r.module }

// AllowsImport reports whether from may import the given path.
func (r *Resolver) AllowsImport(from, importPath string) bool {
	return r.allows(from, importPath, func(c componentRules) matcher { return c.imports })
}

// AllowsExport reports whether from may expose the given path in its API surface.
func (r *Resolver) AllowsExport(from, importPath string) bool {
	return r.allows(from, importPath, func(c componentRules) matcher { return c.exports })
}

func (r *Resolver) allows(from, importPath string, pick func(componentRules) matcher) bool {
	target := r.Component(importPath)
	// A component always reaches itself. Splitting one component across several packages
	// is a layout decision, not an architectural edge.
	if target != "" && target == from {
		return true
	}
	if pick(r.defaults).allows(target, importPath) {
		return true
	}
	rules, ok := r.rules[from]
	if !ok {
		return false
	}
	return pick(rules).allows(target, importPath)
}

// Describe names a target the way a human would: the component if there is one, and the
// import path otherwise. Used in violation messages, where "infra" reads better than the
// package path but an unclassified path has nothing better to offer.
func (r *Resolver) Describe(importPath string) string {
	if c := r.Component(importPath); c != "" {
		return c
	}
	return importPath
}
