package config

import (
	"sort"
	"strings"
)

// Verdict is the outcome of asking whether a component may reach a package.
//
// Three answers rather than a boolean, because "you did not declare this" and "you banned
// this" call for opposite fixes — the first is usually a missing line in arch.yaml, the
// second is a line of Go that has to go — and a message that confused them would send
// people to change the wrong file.
type Verdict int

const (
	// Allowed: permitted, or not constrained at all.
	Allowed Verdict = iota
	// NotDeclared: an allowlist exists and this is not on it.
	NotDeclared
	// Denied: explicitly banned, whatever the allowlists say.
	Denied
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
	deny    matcher
}

// matcher decides whether a target — a component name, or an import path — is permitted.
type matcher struct {
	names    map[string]bool // component names
	exact    map[string]bool // exact import paths
	prefixes []string        // import path prefixes from a trailing /...
	// std allows the whole standard library, via the `std` keyword.
	//
	// Enumerating it is possible and pointless. The first real repository this was run
	// against produced hundreds of findings for `regexp`, `io/fs`, `math` and `cmp` —
	// noise that buries the rules worth having, since no architecture is damaged by a
	// package using the standard library.
	std bool
	// unconstrained means the key was absent from arch.yaml, as opposed to present and
	// empty. The two are different statements: `imports: []` says this component may
	// depend on nothing, while omitting `imports` says nothing at all about it, and a
	// component nobody has thought about yet should not report every line of its own
	// import block. It is what lets the tool be adopted one component at a time.
	unconstrained bool
}

func (m matcher) allows(component, importPath string) bool {
	if component != "" && m.names[component] {
		return true
	}
	if m.std && isStdlib(importPath) {
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
			deny:    r.compile(a, c.Deny),
		}
	}
	// Defaults are additive to every component, so an absent default list contributes
	// nothing. It emphatically does not mean "everything is permitted" — that reading
	// would make omitting `defaults` silently switch the whole tool off.
	r.defaults = componentRules{
		imports: r.compileStrict(a, a.Defaults.Imports),
		exports: r.compileStrict(a, a.Defaults.Exports),
		deny:    r.compileStrict(a, a.Defaults.Deny),
	}

	// Longest prefix first. With `internal/...` and `internal/domain/...` both declared,
	// a package under internal/domain belongs to the more specific one — anything else
	// would make a nested component impossible to express.
	sort.SliceStable(r.owners, func(i, j int) bool {
		return len(r.owners[i].prefix) > len(r.owners[j].prefix)
	})
	return r
}

// compile builds a matcher, treating a nil list as "no constraint".
func (r *Resolver) compile(a *Arch, rules []string) matcher {
	m := r.compileStrict(a, rules)
	m.unconstrained = rules == nil
	return m
}

// compileStrict builds a matcher that permits exactly what it lists, nil included.
func (r *Resolver) compileStrict(a *Arch, rules []string) matcher {
	m := matcher{names: map[string]bool{}, exact: map[string]bool{}}
	for _, rule := range rules {
		if rule == StdKeyword {
			m.std = true
			continue
		}
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

// CheckImport decides whether from may import the given path.
func (r *Resolver) CheckImport(from, importPath string) Verdict {
	if v := r.denied(from, importPath); v == Denied {
		return Denied
	}
	// Permission to export implies permission to import, because the reverse is
	// incoherent: you cannot put time.Time in a signature without importing time, so an
	// export rule that did not carry an import rule with it could never be exercised. The
	// implication runs one way only — importing a package does not entitle you to expose
	// it, which is the entire point of having two lists.
	//
	// Only *listed* exports imply an import, though. An omitted `exports` constrains
	// nothing; it does not grant anything either, so it must not quietly unconstrain the
	// import rule alongside it.
	if r.permitted(from, importPath, func(c componentRules) matcher { return c.imports }) {
		return Allowed
	}
	if rules, ok := r.rules[from]; ok && !rules.exports.unconstrained {
		if r.permitted(from, importPath, func(c componentRules) matcher { return c.exports }) {
			return Allowed
		}
	}
	return NotDeclared
}

// CheckExport decides whether from may expose the given path in its API surface.
func (r *Resolver) CheckExport(from, importPath string) Verdict {
	if v := r.denied(from, importPath); v == Denied {
		return Denied
	}
	if r.permitted(from, importPath, func(c componentRules) matcher { return c.exports }) {
		return Allowed
	}
	return NotDeclared
}

// AllowsImport reports whether from may import the given path.
func (r *Resolver) AllowsImport(from, importPath string) bool {
	return r.CheckImport(from, importPath) == Allowed
}

// AllowsExport reports whether from may expose the given path.
func (r *Resolver) AllowsExport(from, importPath string) bool {
	return r.CheckExport(from, importPath) == Allowed
}

// denied reports whether a target is explicitly banned.
//
// Deny beats every allow, including `std`, including the component's own packages. A ban
// that an allowlist could override would be a suggestion, and the point of writing one is
// to say "not this, whatever else we agreed".
func (r *Resolver) denied(from, importPath string) Verdict {
	target := r.Component(importPath)
	if r.defaults.deny.allows(target, importPath) {
		return Denied
	}
	if rules, ok := r.rules[from]; ok && rules.deny.allows(target, importPath) {
		return Denied
	}
	return Allowed
}

func (r *Resolver) permitted(from, importPath string, pick func(componentRules) matcher) bool {
	target := r.Component(importPath)
	// A component always reaches itself. Splitting one component across several packages
	// is a layout decision, not an architectural edge.
	if target != "" && target == from {
		return true
	}
	rules, ok := r.rules[from]
	if ok && pick(rules).unconstrained {
		return true
	}
	if pick(r.defaults).allows(target, importPath) {
		return true
	}
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
