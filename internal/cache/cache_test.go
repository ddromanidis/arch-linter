package cache

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ddromanidis/arch-linter/internal/report"
)

// write lays out a fake module and returns the packages, in dependency order:
// leaf ← middle ← top.
func chain(t *testing.T) (dir string, pkgs []Package) {
	t.Helper()
	dir = t.TempDir()
	for _, name := range []string{"leaf", "middle", "top"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		f := filepath.Join(dir, name, name+".go")
		if err := os.WriteFile(f, []byte("package "+name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir, []Package{
		{ImportPath: "m/leaf", Files: []string{filepath.Join(dir, "leaf", "leaf.go")}},
		{ImportPath: "m/middle", Files: []string{filepath.Join(dir, "middle", "middle.go")},
			Imports: []string{"m/leaf"}},
		{ImportPath: "m/top", Files: []string{filepath.Join(dir, "top", "top.go")},
			Imports: []string{"m/middle"}},
	}
}

func TestFingerprintsAreStableAndDistinct(t *testing.T) {
	_, pkgs := chain(t)
	a := Fingerprints(pkgs)
	b := Fingerprints(pkgs)

	for _, p := range pkgs {
		if a[p.ImportPath] == "" {
			t.Fatalf("%s has no fingerprint", p.ImportPath)
		}
		if a[p.ImportPath] != b[p.ImportPath] {
			t.Errorf("%s: fingerprint is not stable across runs", p.ImportPath)
		}
	}
	if a["m/leaf"] == a["m/middle"] {
		t.Error("different packages must fingerprint differently")
	}
}

// Changing a package's own file invalidates it.
func TestOwnFileChangeInvalidates(t *testing.T) {
	dir, pkgs := chain(t)
	before := Fingerprints(pkgs)

	if err := os.WriteFile(filepath.Join(dir, "top", "top.go"),
		[]byte("package top\n\nfunc F() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	after := Fingerprints(pkgs)

	if before["m/top"] == after["m/top"] {
		t.Error("top changed, so its fingerprint must change")
	}
	if before["m/leaf"] != after["m/leaf"] {
		t.Error("leaf did not change and must keep its fingerprint")
	}
}

// The subtle one, and the reason fingerprints cover the import graph rather than a single
// package's files. `func F() other.T` is a leak or not depending on what other.T is, so a
// change in a dependency changes this package's answer without touching a byte of it. A
// cache that missed this would report a stale clean run — the worst bug this tool has.
func TestDependencyChangeInvalidatesDependents(t *testing.T) {
	dir, pkgs := chain(t)
	before := Fingerprints(pkgs)

	// Only leaf is edited.
	if err := os.WriteFile(filepath.Join(dir, "leaf", "leaf.go"),
		[]byte("package leaf\n\ntype T struct{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	after := Fingerprints(pkgs)

	for _, p := range []string{"m/leaf", "m/middle", "m/top"} {
		if before[p] == after[p] {
			t.Errorf("%s: fingerprint unchanged, but it depends on leaf transitively", p)
		}
	}
}

// A package outside the module contributes nothing, because go.mod pins it and go.mod is
// part of the configuration key.
func TestExternalImportsAreNotFingerprinted(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.go")
	if err := os.WriteFile(f, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withExternal := []Package{{
		ImportPath: "m/a", Files: []string{f},
		Imports: []string{"gorm.io/gorm", "fmt"},
	}}
	without := []Package{{ImportPath: "m/a", Files: []string{f}}}

	if Fingerprints(withExternal)["m/a"] != Fingerprints(without)["m/a"] {
		t.Error("imports outside the module should not affect the fingerprint")
	}
}

// A malformed graph must not hang. Go forbids import cycles, so this is unreachable in
// practice — but hanging is a worse failure than a cache miss.
func TestCyclicGraphTerminates(t *testing.T) {
	dir := t.TempDir()
	var pkgs []Package
	for _, n := range []string{"a", "b"} {
		f := filepath.Join(dir, n+".go")
		if err := os.WriteFile(f, []byte("package "+n+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		other := "m/b"
		if n == "b" {
			other = "m/a"
		}
		pkgs = append(pkgs, Package{
			ImportPath: "m/" + n, Files: []string{f}, Imports: []string{other},
		})
	}
	if got := Fingerprints(pkgs); len(got) != 2 {
		t.Errorf("got %d fingerprints, want 2", len(got))
	}
}

func finding(pkg string) report.Finding {
	return report.Finding{Rule: "imports", Component: "c", Target: pkg, Message: "x"}
}

func TestRoundTripAndInvalidation(t *testing.T) {
	dir := t.TempDir()
	const version, key = "v1", "cfg"

	c := Open(dir, "m", version, key)
	c.Store("m/a", "fp-1", []report.Finding{finding("x")})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}

	// Same fingerprint: a hit.
	again := Open(dir, "m", version, key)
	got, ok := again.Lookup("m/a", "fp-1")
	if !ok || len(got) != 1 {
		t.Fatalf("expected a hit with one finding, got %v %v", got, ok)
	}

	// Different fingerprint: a miss.
	if _, ok := Open(dir, "m", version, key).Lookup("m/a", "fp-2"); ok {
		t.Error("a changed fingerprint must miss")
	}
	// Different tool version: the whole cache is discarded, because a different build may
	// analyse differently and its results are not ours to trust.
	if _, ok := Open(dir, "m", "v2", key).Lookup("m/a", "fp-1"); ok {
		t.Error("a version change must discard the cache")
	}
	// Different config: likewise.
	if _, ok := Open(dir, "m", version, "other").Lookup("m/a", "fp-1"); ok {
		t.Error("a config change must discard the cache")
	}
}

// An empty result is worth caching: "this package is clean" is exactly the answer you do
// not want to recompute.
func TestCleanPackagesAreCached(t *testing.T) {
	dir := t.TempDir()
	c := Open(dir, "m", "v1", "cfg")
	c.Store("m/a", "fp", nil)
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, ok := Open(dir, "m", "v1", "cfg").Lookup("m/a", "fp")
	if !ok {
		t.Fatal("a clean package should be a hit")
	}
	if len(got) != 0 {
		t.Errorf("got %v, want no findings", got)
	}
}

// Entries not seen this run are dropped, so the file cannot grow forever as packages are
// renamed and deleted.
func TestSavePrunesUnseenEntries(t *testing.T) {
	dir := t.TempDir()
	first := Open(dir, "m", "v1", "cfg")
	first.Store("m/gone", "fp", nil)
	first.Store("m/kept", "fp", nil)
	if err := first.Save(); err != nil {
		t.Fatal(err)
	}

	second := Open(dir, "m", "v1", "cfg")
	second.Lookup("m/kept", "fp") // seen
	if err := second.Save(); err != nil {
		t.Fatal(err)
	}

	third := Open(dir, "m", "v1", "cfg")
	if _, ok := third.Lookup("m/gone", "fp"); ok {
		t.Error("an entry not seen last run should have been pruned")
	}
	if _, ok := third.Lookup("m/kept", "fp"); !ok {
		t.Error("an entry that was used must survive")
	}
}

// A corrupt cache is an empty cache. Failing a build because an optimisation is unreadable
// would be absurd.
func TestCorruptCacheIsIgnored(t *testing.T) {
	dir := t.TempDir()
	c := Open(dir, "m", "v1", "cfg")
	c.Store("m/a", "fp", nil)
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := Open(dir, "m", "v1", "cfg").Lookup("m/a", "fp"); ok {
		t.Error("a corrupt cache must behave as an empty one")
	}
}
