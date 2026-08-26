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
	"strings"
	"testing"
)

const fixture = "analyzer/testdata/src/shop"

// build compiles one of the commands into a temporary directory.
func build(t *testing.T, pkg string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), filepath.Base(pkg))
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
	cmd := exec.Command(cli, "-format", "json", "./internal/infra")
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

	if _, ok := run("-baseline", bl, "./internal/infra"); ok {
		t.Fatal("the fixture violates, so this must fail before a baseline exists")
	}
	if out, ok := run("baseline", "-baseline", bl, "./internal/infra"); !ok {
		t.Fatalf("writing a baseline should succeed: %s", out)
	}
	if _, err := os.Stat(bl); err != nil {
		t.Fatalf("no baseline written: %v", err)
	}
	out, ok := run("-baseline", bl, "./internal/infra")
	if !ok {
		t.Errorf("everything is frozen, so the run must pass:\n%s", out)
	}
	if !strings.Contains(out, "forgiven by the baseline") {
		t.Errorf("expected the forgiven count to be reported:\n%s", out)
	}
}
