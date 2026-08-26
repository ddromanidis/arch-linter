package baseline

import (
	"path/filepath"
	"testing"

	"github.com/ddromanidis/arch-linter/internal/report"
)

func leak(file string, line int) report.Finding {
	return report.Finding{
		Rule: "exports", Component: "infra", Target: "shop/driver",
		File: file, Line: line, Message: "leaks", Severity: "error",
	}
}

// The property the whole design turns on: existing violations are forgiven, one more is
// not. An amnesty that could not tell the difference would let the architecture rot behind
// a green build.
func TestRatchetForgivesExistingAndBlocksNew(t *testing.T) {
	existing := []report.Finding{leak("a.go", 1), leak("a.go", 2), leak("a.go", 3)}
	bl := From(existing)

	kept, forgiven := bl.Apply(existing)
	if len(kept) != 0 || forgiven != 3 {
		t.Fatalf("kept %d, forgave %d; want 0 and 3", len(kept), forgiven)
	}

	// A fourth appears.
	kept, forgiven = bl.Apply(append(existing, leak("a.go", 4)))
	if len(kept) != 1 {
		t.Errorf("kept %d, want 1 — the new one must survive", len(kept))
	}
	if forgiven != 3 {
		t.Errorf("forgave %d, want 3", forgiven)
	}
}

// The reason the key is not file:line. Moving every violation to a different file and line
// must change nothing, or the baseline goes stale on the first refactor and stops being
// believed.
func TestBaselineSurvivesRefactoring(t *testing.T) {
	before := []report.Finding{leak("old/a.go", 10), leak("old/a.go", 20)}
	bl := From(before)

	after := []report.Finding{leak("new/b.go", 99), leak("new/c.go", 1)}
	kept, forgiven := bl.Apply(after)
	if len(kept) != 0 {
		t.Errorf("kept %d, want 0 — the same violations moved, they are not new", len(kept))
	}
	if forgiven != 2 {
		t.Errorf("forgave %d, want 2", forgiven)
	}
}

// A different component, rule or package is a different entry, so a baseline for one
// cannot silence another.
func TestBaselineIsScopedToItsKey(t *testing.T) {
	bl := From([]report.Finding{leak("a.go", 1)})

	other := report.Finding{
		Rule: "exports", Component: "app", Target: "shop/driver",
		File: "b.go", Line: 1, Message: "leaks", Severity: "error",
	}
	kept, _ := bl.Apply([]report.Finding{other})
	if len(kept) != 1 {
		t.Error("infra's baseline must not forgive app")
	}

	differentRule := leak("a.go", 1)
	differentRule.Rule = "imports"
	kept, _ = bl.Apply([]report.Finding{differentRule})
	if len(kept) != 1 {
		t.Error("an exports baseline must not forgive an imports violation")
	}

	differentTarget := leak("a.go", 1)
	differentTarget.Target = "other.io/lib"
	kept, _ = bl.Apply([]report.Finding{differentTarget})
	if len(kept) != 1 {
		t.Error("a baseline about the driver must not forgive a different package")
	}
}

// Fixing violations should prompt you to lock the improvement in, or the baseline stays
// permanently looser than the code needs.
func TestStaleEntriesAreReported(t *testing.T) {
	bl := From([]report.Finding{leak("a.go", 1), leak("a.go", 2), leak("a.go", 3)})

	if stale := bl.Stale([]report.Finding{leak("a.go", 1), leak("a.go", 2), leak("a.go", 3)}); len(stale) != 0 {
		t.Errorf("nothing was fixed, so nothing is stale: %v", stale)
	}
	stale := bl.Stale([]report.Finding{leak("a.go", 1)})
	if len(stale) != 1 || stale[0].Count != 3 {
		t.Errorf("two were fixed, so the entry over-counts: %v", stale)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".archlint-baseline.yaml")
	findings := []report.Finding{leak("a.go", 1), leak("a.go", 2)}
	if err := Save(path, findings); err != nil {
		t.Fatal(err)
	}
	bl, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(bl.Entries) != 1 || bl.Entries[0].Count != 2 {
		t.Fatalf("round trip lost the counts: %+v", bl.Entries)
	}
	kept, forgiven := bl.Apply(findings)
	if len(kept) != 0 || forgiven != 2 {
		t.Errorf("a loaded baseline must behave like a fresh one: kept %d, forgave %d",
			len(kept), forgiven)
	}
}

// A missing baseline forgives nothing. Silence should have to be asked for.
func TestMissingBaselineForgivesNothing(t *testing.T) {
	bl, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	kept, forgiven := bl.Apply([]report.Finding{leak("a.go", 1)})
	if len(kept) != 1 || forgiven != 0 {
		t.Errorf("kept %d, forgave %d; want 1 and 0", len(kept), forgiven)
	}
}
