package config

import "testing"

// Omitting a rule list and writing it empty are different statements. This is the whole
// point of the distinction: `imports: []` locks a component down, omitting `imports` says
// nothing about it, and the second is what lets the tool be adopted a component at a time
// instead of all at once.
func TestOmittedMeansUnconstrainedEmptyMeansLocked(t *testing.T) {
	a := arch(t, `
components:
  locked:
    path: l/...
    imports: []
  loose:
    path: o/...
  strict:
    path: s/...
    imports: [locked]
`)
	r := NewResolver(a, "")

	// Omitted: anything goes.
	for _, p := range []string{"gorm.io/gorm", "fmt", "example.test/m/l", "any.io/thing"} {
		if !r.AllowsImport("loose", p) {
			t.Errorf("loose omits `imports`, so %q should be allowed", p)
		}
	}
	// Explicitly empty: nothing but its own packages.
	for _, p := range []string{"gorm.io/gorm", "fmt", "example.test/m/s"} {
		if r.AllowsImport("locked", p) {
			t.Errorf("locked declares `imports: []`, so %q must be refused", p)
		}
	}
	if !r.AllowsImport("locked", "example.test/m/l/sub") {
		t.Error("a component always reaches its own packages")
	}
	// A declared list still means what it says.
	if !r.AllowsImport("strict", "example.test/m/l") {
		t.Error("strict declares locked")
	}
	if r.AllowsImport("strict", "gorm.io/gorm") {
		t.Error("strict declared a list, so anything off it is refused")
	}
}

// The same for exports, which matters more: most components have no API surface worth
// constraining, and requiring each to say so was pure noise.
func TestOmittedExportsAreUnconstrained(t *testing.T) {
	a := arch(t, `
components:
  loose:
    path: o/...
    imports: [std, "gorm.io/gorm"]
  tight:
    path: t/...
    imports: [std, "gorm.io/gorm"]
    exports: []
`)
	r := NewResolver(a, "")
	if !r.AllowsExport("loose", "gorm.io/gorm") {
		t.Error("loose omits `exports`, so exposing gorm is not checked")
	}
	if r.AllowsExport("tight", "gorm.io/gorm") {
		t.Error("tight declares `exports: []`, so exposing gorm is a violation")
	}
}

// An omitted `exports` must not silently unconstrain imports through the
// export-implies-import rule. It grants nothing; it just declines to check.
func TestUnconstrainedExportsDoNotUnconstrainImports(t *testing.T) {
	a := arch(t, `
components:
  a:
    path: a/...
    imports: []
`)
	r := NewResolver(a, "")
	if r.AllowsImport("a", "gorm.io/gorm") {
		t.Error("`imports: []` is a lockdown; an absent `exports` must not undo it")
	}
}

// Deny beats every allow, including std and including an unconstrained component — which
// is the only rule that still applies to one.
func TestDenyBeatsEveryAllow(t *testing.T) {
	a := arch(t, `
components:
  everything:
    path: e/...
    imports: [std, "gorm.io/gorm"]
    deny: ["gorm.io/gorm", os/exec]
  loose:
    path: o/...
    deny: ["gorm.io/gorm"]
defaults:
  imports: [std]
`)
	r := NewResolver(a, "")

	if r.AllowsImport("everything", "gorm.io/gorm") {
		t.Error("deny must beat an explicit allow")
	}
	if r.AllowsImport("everything", "os/exec") {
		t.Error("deny must beat std")
	}
	if !r.AllowsImport("everything", "net/http") {
		t.Error("the rest of std is untouched")
	}
	// The combination the feature exists for: unconstrained, except for this one ban.
	if r.AllowsImport("loose", "gorm.io/gorm") {
		t.Error("deny must apply to a component with no allowlist")
	}
	if !r.AllowsImport("loose", "anything.io/else") {
		t.Error("an unconstrained component still permits everything else")
	}
}

// Denying an import denies exposing it too — you cannot expose what you may not import.
func TestDenyAppliesToExportsAsWell(t *testing.T) {
	a := arch(t, `
components:
  a:
    path: a/...
    deny: ["gorm.io/gorm"]
`)
	r := NewResolver(a, "")
	if r.AllowsExport("a", "gorm.io/gorm") {
		t.Error("a denied package must not be exportable either")
	}
}

// Project-wide bans belong in defaults rather than repeated per component.
func TestDefaultsDenyAppliesEverywhere(t *testing.T) {
	a := arch(t, `
components:
  a:
    path: a/...
    imports: [std]
  b:
    path: b/...
defaults:
  imports: [std]
  deny: [unsafe]
`)
	r := NewResolver(a, "")
	for _, c := range []string{"a", "b"} {
		if r.AllowsImport(c, "unsafe") {
			t.Errorf("%s: a project-wide deny reaches every component", c)
		}
		if !r.AllowsImport(c, "fmt") {
			t.Errorf("%s: and bans nothing else", c)
		}
	}
}

// Deny takes prefixes, so a whole module can go at once.
func TestDenyPrefix(t *testing.T) {
	a := arch(t, `
components:
  a:
    path: a/...
    deny: ["github.com/old/lib/..."]
`)
	r := NewResolver(a, "")
	if r.AllowsImport("a", "github.com/old/lib") {
		t.Error("the module root is denied")
	}
	if r.AllowsImport("a", "github.com/old/lib/deep") {
		t.Error("and everything under it")
	}
	if !r.AllowsImport("a", "github.com/old/library") {
		t.Error("but not a package that merely shares a prefix")
	}
}

// The verdict has to distinguish the two failures, because the fixes are opposite: delete
// a line of Go, or add a line to arch.yaml.
func TestVerdictDistinguishesDeniedFromUndeclared(t *testing.T) {
	a := arch(t, `
components:
  a:
    path: a/...
    imports: [std]
    deny: [unsafe]
`)
	r := NewResolver(a, "")
	if got := r.CheckImport("a", "unsafe"); got != Denied {
		t.Errorf("unsafe: got %v, want Denied", got)
	}
	if got := r.CheckImport("a", "gorm.io/gorm"); got != NotDeclared {
		t.Errorf("gorm: got %v, want NotDeclared", got)
	}
	if got := r.CheckImport("a", "fmt"); got != Allowed {
		t.Errorf("fmt: got %v, want Allowed", got)
	}
}

// Omitting `defaults` entirely must not switch the tool off. Defaults are additive to each
// component, so an absent list contributes nothing rather than permitting everything.
func TestOmittedDefaultsGrantNothing(t *testing.T) {
	a := arch(t, `
components:
  a:
    path: a/...
    imports: []
`)
	r := NewResolver(a, "")
	if r.AllowsImport("a", "fmt") {
		t.Error("no defaults means no defaults, not free rein")
	}
}
