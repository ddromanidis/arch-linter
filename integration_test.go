// Package archlint_test checks that the ways of running the linter agree with each other.
//
// The whole design rests on there being one implementation behind three front ends. That is
// only true as long as nobody wires one of them up differently, and a wiring mistake is
// invisible to every unit test in the repository — each front end would still work, they
// would just disagree. So the CLI and the vet tool are built and run against the same
// fixture, and their findings compared.
package archlint_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const fixture = "analyzer/testdata/src/shop"

// build compiles one of the commands into a temporary directory.
func build(t *testing.T, pkg string) string {
	t.Helper()
	name := filepath.Base(pkg)
	// Windows will not execute a file without the extension, and `go build -o` writes
	// exactly the name it is given rather than adding one. Without this the binary builds
	// and then cannot be run, which failed only on Windows and only in CI.
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(t.TempDir(), name)
	out, err := exec.Command("go", "build", "-o", bin, "./"+pkg).CombinedOutput()
	if err != nil {
		t.Fatalf("building %s: %v\n%s", pkg, err, out)
	}
	return bin
}

type finding struct {
	Rule    string `json:"rule"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// The CLI and `go vet -vettool` must report the same violations. They format and exit
// differently on purpose; what they *find* may not differ at all.
func TestCLIAndVetToolAgree(t *testing.T) {
	if testing.Short() {
		t.Skip("builds two binaries and shells out")
	}
	cli := build(t, "cmd/archlint")
	vet := build(t, "cmd/archlint-vet")

	dir, err := filepath.Abs(fixture)
	if err != nil {
		t.Fatal(err)
	}

	// The CLI, as JSON so the comparison is on structure rather than on formatting.
	cmd := exec.Command(cli, "--format", "json", "./internal/infra")
	cmd.Dir = dir
	stdout, _ := cmd.Output() // exit 1 is expected: the fixture violates on purpose
	var cliFindings []finding
	if err := json.Unmarshal(stdout, &cliFindings); err != nil {
		t.Fatalf("parsing CLI json: %v\n%s", err, stdout)
	}
	if len(cliFindings) == 0 {
		t.Fatal("the CLI found nothing, so this test proves nothing")
	}

	// The vet tool, whose output is one `file:line:col: message` per line.
	cmd = exec.Command("go", "vet", "-vettool="+vet, "./internal/infra")
	cmd.Dir = dir
	combined, _ := cmd.CombinedOutput()

	// Every message the CLI reports must appear in vet's output.
	vetText := string(combined)
	for _, f := range cliFindings {
		if !strings.Contains(vetText, f.Message) {
			t.Errorf("the CLI reported %q but the vet tool did not:\n%s", f.Message, vetText)
		}
	}

	// And the counts must match, so vet reporting *extra* things is caught too.
	var vetLines int
	for line := range strings.Lines(vetText) {
		if strings.Contains(line, "shop/driver") || strings.Contains(line, "may not import") {
			vetLines++
		}
	}
	if vetLines != len(cliFindings) {
		t.Errorf("CLI found %d violations, vet found %d:\n%s",
			len(cliFindings), vetLines, vetText)
	}
}

// A clean package must be silent and exit 0 through the CLI, because that is the state a
// repository spends nearly all of its time in and the one people notice being wrong.
func TestCleanRunExitsZero(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary and shells out")
	}
	cli := build(t, "cmd/archlint")
	dir, err := filepath.Abs(fixture)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(cli, "./internal/domain")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("a clean package must exit 0, got %v:\n%s", err, out)
	}
	if !strings.Contains(string(out), "no violations") {
		t.Errorf("unexpected output: %s", out)
	}
}

// The baseline round trip, through the real binary rather than the package API: freeze,
// re-run clean, then break it again.
func TestBaselineRoundTripThroughTheCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary and shells out")
	}
	cli := build(t, "cmd/archlint")
	dir, err := filepath.Abs(fixture)
	if err != nil {
		t.Fatal(err)
	}
	bl := filepath.Join(t.TempDir(), "baseline.yaml")

	run := func(args ...string) (string, bool) {
		cmd := exec.Command(cli, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return string(out), err == nil
	}

	if _, ok := run("--baseline", bl, "./internal/infra"); ok {
		t.Fatal("the fixture violates, so this must fail before a baseline exists")
	}
	if out, ok := run("baseline", "--baseline", bl, "./internal/infra"); !ok {
		t.Fatalf("writing a baseline should succeed: %s", out)
	}
	if _, err := os.Stat(bl); err != nil {
		t.Fatalf("no baseline written: %v", err)
	}
	out, ok := run("--baseline", bl, "./internal/infra")
	if !ok {
		t.Errorf("everything is frozen, so the run must pass:\n%s", out)
	}
	if !strings.Contains(out, "forgiven by the baseline") {
		t.Errorf("expected the forgiven count to be reported:\n%s", out)
	}
}

// A cached run and an uncached one must report exactly the same thing.
//
// This is the property the cache lives or dies by, and it is the one that catches the
// failure that matters: a cache which quietly analyses less. An early version of
// --no-cache loaded packages without type information and then skipped every one of them,
// reporting nothing at all — and every other test still passed, because they all went
// through the cached path.
func TestCachedAndUncachedRunsAgree(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary and shells out")
	}
	cli := build(t, "cmd/archlint")
	dir, err := filepath.Abs(fixture)
	if err != nil {
		t.Fatal(err)
	}

	runJSON := func(extra ...string) []finding {
		args := append([]string{"--format", "json"}, extra...)
		cmd := exec.Command(cli, append(args, "./...")...)
		cmd.Dir = dir
		out, _ := cmd.Output() // exit 1 is expected: the fixture violates on purpose
		var got []finding
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("parsing json (%v): %v\n%s", extra, err, out)
		}
		return got
	}

	uncached := runJSON("--no-cache")
	if len(uncached) == 0 {
		t.Fatal("the uncached run found nothing, so this test proves nothing")
	}

	// First cached run populates, second reads back.
	_ = runJSON()
	cached := runJSON()

	if len(cached) != len(uncached) {
		t.Fatalf("cached found %d, uncached %d", len(cached), len(uncached))
	}
	for i := range uncached {
		if cached[i] != uncached[i] {
			t.Errorf("finding %d differs:\n cached   %+v\n uncached %+v",
				i, cached[i], uncached[i])
		}
	}
}

// Editing a file must invalidate it, or the cache reports a clean run it has not earned.
func TestCacheInvalidatesOnEdit(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary, shells out, and edits a fixture")
	}
	cli := build(t, "cmd/archlint")
	dir, err := filepath.Abs(fixture)
	if err != nil {
		t.Fatal(err)
	}

	count := func() int {
		cmd := exec.Command(cli, "--format", "json", "./...")
		cmd.Dir = dir
		out, _ := cmd.Output()
		var got []finding
		_ = json.Unmarshal(out, &got)
		return len(got)
	}

	before := count()
	if before == 0 {
		t.Fatal("the fixture should violate on purpose")
	}
	_ = count() // warm

	// Add a leak to a package that was clean, and restore it whatever happens.
	path := filepath.Join(dir, "internal", "domain", "extra_leak.go")
	body := "package domain\n\nimport \"shop/driver\"\n\nfunc Leak() *driver.DB { return nil }\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	if after := count(); after <= before {
		t.Errorf("added a leak but the count went %d → %d; the cache is stale", before, after)
	}
}
