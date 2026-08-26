package config

import (
	"strings"
	"testing"
)

func arch(t *testing.T, yaml string) *Arch {
	t.Helper()
	a, err := parseArch([]byte("version: 1\nmodule: example.test/m\n"+yaml), "arch.yaml")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return a
}

// A clean layering has no cycles, and must not be reported as having one.
func TestNoCyclesInALayeredGraph(t *testing.T) {
	a := arch(t, `
components:
  domain:
    path: d/...
  app:
    path: a/...
    imports: [domain]
  infra:
    path: i/...
    imports: [domain, app]
  cmd:
    path: c/...
    imports: [domain, app, infra]
`)
	if got := a.Cycles(); len(got) != 0 {
		t.Errorf("a layered graph has no cycles, got %v", got)
	}
}

// The two-component case: mutual dependency. Go permits this between components because
// each is several packages, so no single package imports itself transitively.
func TestDirectCycle(t *testing.T) {
	a := arch(t, `
components:
  a:
    path: a/...
    imports: [b]
  b:
    path: b/...
    imports: [a]
`)
	got := a.Cycles()
	if len(got) != 1 {
		t.Fatalf("want exactly one cycle, got %v", got)
	}
	if s := got[0].String(); s != "a → b → a" {
		t.Errorf("cycle = %q, want %q", s, "a → b → a")
	}
}

// The case that motivates the check: a loop long enough that nobody spots it in review.
func TestIndirectCycle(t *testing.T) {
	a := arch(t, `
components:
  app:
    path: app/...
    imports: [events]
  events:
    path: ev/...
    imports: [support]
  support:
    path: sup/...
    imports: [app]
`)
	got := a.Cycles()
	if len(got) != 1 {
		t.Fatalf("want one cycle, got %v", got)
	}
	if s := got[0].String(); s != "app → events → support → app" {
		t.Errorf("cycle = %q", s)
	}
}

// Exporting a component implies importing it, so a loop closed through `exports` is still
// a loop. Missing this would leave the easiest cycle to write undetected.
func TestCycleThroughExports(t *testing.T) {
	a := arch(t, `
components:
  a:
    path: a/...
    imports: [b]
  b:
    path: b/...
    exports: [a]
`)
	if got := a.Cycles(); len(got) != 1 {
		t.Errorf("a cycle closed through exports is still a cycle, got %v", got)
	}
}

// One loop is one finding, however many ways the walk can enter it. Reporting A→B→A and
// B→A→B separately would double every count and depend on nothing but alphabetical order.
func TestCycleReportedOnce(t *testing.T) {
	a := arch(t, `
components:
  a:
    path: a/...
    imports: [b, c]
  b:
    path: b/...
    imports: [c]
  c:
    path: c/...
    imports: [a]
`)
	got := a.Cycles()
	// a→b→c→a and a→c→a are genuinely different loops; neither may appear twice.
	seen := map[string]bool{}
	for _, cyc := range got {
		if seen[cyc.String()] {
			t.Errorf("cycle %v reported more than once", cyc)
		}
		seen[cyc.String()] = true
	}
	if len(got) == 0 {
		t.Error("expected at least one cycle")
	}
}

// Determinism. The same file must produce the same message every run, or a failing build
// reports something different each time it is re-run.
func TestCycleOutputIsDeterministic(t *testing.T) {
	src := `
components:
  zebra:
    path: z/...
    imports: [alpha]
  alpha:
    path: a/...
    imports: [middle]
  middle:
    path: m/...
    imports: [zebra]
`
	var first []string
	for range 20 {
		a := arch(t, src)
		var got []string
		for _, c := range a.Cycles() {
			got = append(got, c.String())
		}
		if first == nil {
			first = got
			continue
		}
		if strings.Join(got, "|") != strings.Join(first, "|") {
			t.Fatalf("cycles differ between runs:\n %v\n %v", first, got)
		}
	}
	if len(first) != 1 {
		t.Errorf("want one cycle, got %v", first)
	}
}

// A rule naming a non-component — a third-party package — is not an edge in the graph.
func TestExternalRulesAreNotEdges(t *testing.T) {
	a := arch(t, `
components:
  a:
    path: a/...
    imports: ["gorm.io/gorm", std]
`)
	if got := a.Cycles(); len(got) != 0 {
		t.Errorf("external packages are not components, got %v", got)
	}
}

// The diagnostic points at the architecture, so the line has to be right.
func TestComponentLines(t *testing.T) {
	a := arch(t, `
components:
  first:
    path: f/...
  second:
    path: s/...
`)
	// Lines 1-2 are the version and module prefix, 3 is the fixture's leading blank,
	// 4 is `components:`, so `first:` is 5 and `second:` is 7.
	if got := a.ComponentLine("first"); got != 5 {
		t.Errorf("first is on line %d, want 5", got)
	}
	if got := a.ComponentLine("second"); got != 7 {
		t.Errorf("second is on line %d, want 7", got)
	}
	if got := a.ComponentLine("absent"); got != 1 {
		t.Errorf("an unknown component falls back to line %d, want 1", got)
	}
}
