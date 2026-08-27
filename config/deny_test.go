package config

import (
	"strings"
	"testing"
)

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

// Per-component severity is what makes a rule adoptable layer by layer: domain at error
// because it is already clean, adapters at warning because it is a goal. A baseline says
// "not these existing violations"; only this says "this layer is aspirational".
func TestPerComponentSeverityOverridesGlobal(t *testing.T) {
	a := arch(t, `
components:
  domain:
    path: d/...
    imports: []
  adapters:
    path: a/...
    imports: []
    severity:
      exports: warning
      imports: "off"
`)
	global := Severities{Imports: Error, Exports: Error, Waivers: Warning}

	// A component that says nothing keeps the global settings.
	kept := global.Override(a.Components["domain"].Severity)
	if kept.Imports != Error || kept.Exports != Error {
		t.Errorf("domain should keep the global severities, got %+v", kept)
	}

	over := global.Override(a.Components["adapters"].Severity)
	if over.Exports != Warning {
		t.Errorf("exports = %q, want warning", over.Exports)
	}
	if over.Imports != Off {
		t.Errorf("imports = %q, want off", over.Imports)
	}
	// Unmentioned rules are untouched.
	if over.Waivers != Warning {
		t.Errorf("waivers = %q, want the global warning", over.Waivers)
	}
}

func TestInvalidComponentSeverityIsRejected(t *testing.T) {
	_, err := parseArch([]byte(`
version: 1
components:
  a:
    path: a/...
    severity:
      exports: loud
`), "arch.yaml")
	if err == nil || !strings.Contains(err.Error(), "severity.exports") {
		t.Errorf("got %v, want a complaint about severity.exports", err)
	}
}

// explain has to name the rule that decided, not merely the outcome — that is the whole
// reason it exists rather than a second way to ask the same yes/no.
func TestExplainNamesTheDecidingRule(t *testing.T) {
	a := arch(t, `
components:
  infra:
    path: i/...
    imports: [std, "gorm.io/gorm", domain]
    exports: [domain]
    deny: [unsafe]
  domain:
    path: d/...
    imports: []
  loose:
    path: l/...
defaults:
  imports: [fmt]
`)
	r := NewResolver(a, "")

	for _, tc := range []struct {
		name          string
		got           Reason
		wantVerdict   Verdict
		wantRule      string
		wantSourceHas string
	}{
		{"explicit allow", r.ExplainImport("infra", "gorm.io/gorm"),
			Allowed, "gorm.io/gorm", "infra.imports"},
		{"component name", r.ExplainImport("infra", "example.test/m/d"),
			Allowed, "domain", "infra.imports"},
		{"std keyword", r.ExplainImport("infra", "net/http"),
			Allowed, StdKeyword, "infra.imports"},
		{"defaults", r.ExplainImport("domain", "fmt"),
			Allowed, "fmt", "defaults.imports"},
		{"deny wins", r.ExplainImport("infra", "unsafe"),
			Denied, "unsafe", "infra.deny"},
		{"not on any list", r.ExplainExport("infra", "gorm.io/gorm"),
			NotDeclared, ReasonNotOnAnyList, ""},
		{"same component", r.ExplainImport("domain", "example.test/m/d/sub"),
			Allowed, ReasonSameComponent, ""},
		{"unconstrained", r.ExplainImport("loose", "anything.io/x"),
			Allowed, ReasonUnconstrained, "loose.imports"},
		{"unknown component", r.ExplainImport("nope", "fmt"),
			NotDeclared, ReasonUnknown, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got.Verdict != tc.wantVerdict {
				t.Errorf("verdict = %v, want %v", tc.got.Verdict, tc.wantVerdict)
			}
			if tc.got.Rule != tc.wantRule {
				t.Errorf("rule = %q, want %q", tc.got.Rule, tc.wantRule)
			}
			if tc.wantSourceHas != "" && !strings.Contains(tc.got.Source, tc.wantSourceHas) {
				t.Errorf("source = %q, want it to mention %q", tc.got.Source, tc.wantSourceHas)
			}
		})
	}
}

// The subtle one: an import permitted only because exporting implies importing should say
// so, rather than claiming an import rule allowed it.
func TestExplainSurfacesTheExportImplication(t *testing.T) {
	a := arch(t, `
components:
  a:
    path: a/...
    imports: []
    exports: [time]
`)
	r := NewResolver(a, "")
	got := r.ExplainImport("a", "time")
	if got.Verdict != Allowed {
		t.Fatalf("verdict = %v, want Allowed", got.Verdict)
	}
	if !strings.Contains(got.Source, "exports") {
		t.Errorf("source = %q, want it to attribute the export rule", got.Source)
	}
}
