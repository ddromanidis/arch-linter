package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// sample is deliberately awkward: two files, out of order, mixed severities, an absolute
// path that must be shortened, and a message containing the characters the GitHub format
// has to escape.
func sample() []Finding {
	return []Finding{
		{
			Rule: "exports", Component: "infra", Target: "gorm.io/gorm",
			File: "/repo/internal/infra/b.go", Line: 40, Column: 6,
			Message:  "Repo.Raw exposes gorm.io/gorm, which infra may not export",
			Severity: "error",
		},
		{
			Rule: "imports", Component: "app", Target: "gorm.io/gorm",
			File: "/repo/internal/app/a.go", Line: 9, Column: 2,
			Message:  "app may not import gorm.io/gorm",
			Severity: "warning",
		},
		{
			Rule: "imports", Component: "app", Target: "unsafe",
			File: "/repo/internal/app/a.go", Line: 3, Column: 2,
			Message:  "app may not import unsafe (denied)",
			Severity: "error",
		},
	}
}

const root = "/repo"

func write(t *testing.T, format string, findings []Finding) string {
	t.Helper()
	var b bytes.Buffer
	if err := Write(&b, format, findings, root, false); err != nil {
		t.Fatalf("%s: %v", format, err)
	}
	return b.String()
}

// Every format must order findings by file then position. Without it two runs on unchanged
// code produce different diffs, and nobody can tell a real change from map iteration order.
func TestOutputIsSortedAndDeterministic(t *testing.T) {
	for _, format := range []string{"text", "json", "github", "sarif"} {
		first := write(t, format, sample())
		for range 5 {
			if got := write(t, format, sample()); got != first {
				t.Fatalf("%s: output differs between runs", format)
			}
		}
		// a.go:3 before a.go:9 before b.go:40.
		three := strings.Index(first, "a.go")
		forty := strings.LastIndex(first, "b.go")
		if three < 0 || forty < 0 || three > forty {
			t.Errorf("%s: not sorted by file:\n%s", format, first)
		}
	}
}

// Paths are shortened against the project root in every format. A report full of one
// developer's home directory is not portable to the machine reading it.
func TestPathsAreRelativeEverywhere(t *testing.T) {
	for _, format := range []string{"text", "json", "github", "sarif"} {
		out := write(t, format, sample())
		if strings.Contains(out, "/repo/internal") {
			t.Errorf("%s: absolute paths leaked:\n%s", format, out)
		}
		if !strings.Contains(out, "internal/app/a.go") {
			t.Errorf("%s: relative path missing:\n%s", format, out)
		}
		// Forward slashes on every platform. A SARIF uri is a URI and a GitHub
		// workflow command's file= is matched against forward-slash paths, so a
		// backslash makes the annotation silently not appear.
		if strings.Contains(out, `\\`) {
			t.Errorf("%s: backslash separator in output:\n%s", format, out)
		}
	}
}

func TestTextCountsErrorsAndWarningsSeparately(t *testing.T) {
	out := write(t, "text", sample())
	if !strings.Contains(out, "2 error(s), 1 warning(s)") {
		t.Errorf("wrong tally:\n%s", out)
	}
	if !strings.Contains(out, "[exports]") || !strings.Contains(out, "[imports]") {
		t.Errorf("the rule name should be shown:\n%s", out)
	}
}

func TestTextSaysSoWhenClean(t *testing.T) {
	out := write(t, "text", nil)
	if !strings.Contains(out, "no violations") {
		t.Errorf("a clean run must say so, got %q", out)
	}
}

// A consumer scripting against the JSON should get an empty list on a clean run, not a
// `null` it has to special-case.
func TestJSONIsAlwaysAList(t *testing.T) {
	out := write(t, "json", nil)
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("clean run produced %q, want []", strings.TrimSpace(out))
	}
	var decoded []Finding
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("not valid json: %v", err)
	}
}

func TestJSONRoundTripsEveryField(t *testing.T) {
	out := write(t, "json", sample())
	var got []Finding
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not valid json: %v\n%s", err, out)
	}
	if len(got) != 3 {
		t.Fatalf("got %d findings, want 3", len(got))
	}
	first := got[0]
	if first.File != "internal/app/a.go" || first.Line != 3 || first.Column != 2 {
		t.Errorf("position lost: %+v", first)
	}
	// Target is what a baseline keys on, so losing it in serialisation would break the
	// ratchet for anyone consuming the JSON.
	if first.Target != "unsafe" || first.Rule != "imports" || first.Component != "app" {
		t.Errorf("identity fields lost: %+v", first)
	}
	if first.Severity != "error" {
		t.Errorf("severity lost: %+v", first)
	}
}

// GitHub workflow commands put findings on the diff of a pull request. The format is
// positional and unforgiving: one wrong separator and the annotation silently does not
// appear.
func TestGitHubWorkflowCommands(t *testing.T) {
	out := write(t, "github", sample())
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("want one line per finding, got %d:\n%s", len(lines), out)
	}
	want := "::error file=internal/app/a.go,line=3,col=2::app may not import unsafe (denied)"
	if lines[0] != want {
		t.Errorf("got  %q\nwant %q", lines[0], want)
	}
	// Severity maps to the annotation level, or a warning shows up as an error on the PR.
	if !strings.HasPrefix(lines[1], "::warning ") {
		t.Errorf("warning should map to ::warning, got %q", lines[1])
	}
}

// Percent and newline are the two characters that corrupt a workflow command.
func TestGitHubEscapesDangerousCharacters(t *testing.T) {
	out := write(t, "github", []Finding{{
		Rule: "imports", Component: "a", File: "/repo/a.go", Line: 1, Column: 1,
		Message: "100% wrong\nsecond line", Severity: "error",
	}})
	if strings.Contains(out, "100% wrong") {
		t.Errorf("percent not escaped: %q", out)
	}
	if strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Errorf("newline not escaped, the command is now two lines: %q", out)
	}
	if !strings.Contains(out, "%25") || !strings.Contains(out, "%0A") {
		t.Errorf("expected %%25 and %%0A escapes: %q", out)
	}
}

// SARIF is the one that fails silently. GitHub code scanning ingests a malformed document
// as zero results — no error, no findings, a green tick — so the shape is asserted rather
// than assumed.
func TestSARIFHasTheShapeCodeScanningExpects(t *testing.T) {
	out := write(t, "sarif", sample())

	// Assert on the raw bytes before decoding. SARIF keys are case-sensitive and Go's
	// unmarshaller is not, so decoding into a tagged struct happily accepts "Text" where
	// the schema demands "text" — which is how this shipped emitting the wrong key.
	for _, key := range []string{
		`"version"`, `"$schema"`, `"runs"`, `"tool"`, `"driver"`, `"results"`,
		`"ruleId"`, `"level"`, `"message"`, `"text"`, `"locations"`,
		`"physicalLocation"`, `"artifactLocation"`, `"uri"`, `"region"`,
		`"startLine"`, `"startColumn"`,
	} {
		if !strings.Contains(out, key) {
			t.Errorf("SARIF is missing the key %s:\n%s", key, out)
		}
	}
	// The specific casing bug: a capital anywhere in a key means an untagged struct field.
	for _, wrong := range []string{`"Text"`, `"RuleID"`, `"Level"`, `"Message"`, `"Locations"`} {
		if strings.Contains(out, wrong) {
			t.Errorf("SARIF key %s is capitalised; the schema is case-sensitive", wrong)
		}
	}

	var doc struct {
		Version string `json:"version"`
		Schema  string `json:"$schema"`
		Runs    []struct {
			Tool struct {
				Driver struct {
					Name           string `json:"name"`
					InformationURI string `json:"informationUri"`
				} `json:"driver"`
			} `json:"tool"`
			Results []struct {
				RuleID  string `json:"ruleId"`
				Level   string `json:"level"`
				Message struct {
					Text string `json:"text"`
				} `json:"message"`
				Locations []struct {
					PhysicalLocation struct {
						ArtifactLocation struct {
							URI string `json:"uri"`
						} `json:"artifactLocation"`
						Region struct {
							StartLine   int `json:"startLine"`
							StartColumn int `json:"startColumn"`
						} `json:"region"`
					} `json:"physicalLocation"`
				} `json:"locations"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not valid json: %v\n%s", err, out)
	}

	if doc.Version != "2.1.0" {
		t.Errorf("version = %q, want 2.1.0", doc.Version)
	}
	if !strings.Contains(doc.Schema, "sarif-2.1.0") {
		t.Errorf("$schema = %q", doc.Schema)
	}
	if len(doc.Runs) != 1 {
		t.Fatalf("want exactly one run, got %d", len(doc.Runs))
	}
	runOne := doc.Runs[0]
	if runOne.Tool.Driver.Name != "arch-lint" && runOne.Tool.Driver.Name != "archlint" {
		t.Errorf("driver name = %q", runOne.Tool.Driver.Name)
	}
	if runOne.Tool.Driver.InformationURI == "" {
		t.Error("informationUri is required for the tool to be linkable")
	}
	if len(runOne.Results) != 3 {
		t.Fatalf("want 3 results, got %d", len(runOne.Results))
	}

	first := runOne.Results[0]
	// SARIF levels are a closed set. Anything else is dropped on ingest.
	for _, r := range runOne.Results {
		switch r.Level {
		case "error", "warning", "note", "none":
		default:
			t.Errorf("level %q is not a SARIF level", r.Level)
		}
	}
	if first.RuleID != "archlint/imports" {
		t.Errorf("ruleId = %q", first.RuleID)
	}
	if first.Message.Text == "" {
		t.Error("message.text is required")
	}
	if len(first.Locations) != 1 {
		t.Fatalf("want one location, got %d", len(first.Locations))
	}
	loc := first.Locations[0].PhysicalLocation
	if loc.ArtifactLocation.URI != "internal/app/a.go" {
		t.Errorf("uri = %q, want a repo-relative path", loc.ArtifactLocation.URI)
	}
	// SARIF regions are 1-based; a zero here is rejected by the schema.
	if loc.Region.StartLine != 3 || loc.Region.StartColumn != 2 {
		t.Errorf("region = %+v, want line 3 column 2", loc.Region)
	}
}

// A clean run still has to be a valid document, or CI uploading it fails on the good days.
func TestSARIFIsValidWhenEmpty(t *testing.T) {
	out := write(t, "sarif", nil)
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not valid json: %v\n%s", err, out)
	}
	runs, ok := doc["runs"].([]any)
	if !ok || len(runs) != 1 {
		t.Fatalf("runs missing or wrong: %v", doc["runs"])
	}
	results := runs[0].(map[string]any)["results"]
	if results == nil {
		t.Error("results must be an empty array, not absent or null")
	}
}

func TestUnknownFormatIsAnError(t *testing.T) {
	var b bytes.Buffer
	if err := Write(&b, "xml", sample(), root, false); err == nil {
		t.Fatal("expected an error for an unknown format")
	}
}

// Colour is for a terminal. Escape codes in a piped log are worse than no colour.
func TestColourOnlyWhenAsked(t *testing.T) {
	var plain, coloured bytes.Buffer
	_ = Write(&plain, "text", sample(), root, false)
	_ = Write(&coloured, "text", sample(), root, true)
	if strings.Contains(plain.String(), "\x1b[") {
		t.Error("escape codes present with colour off")
	}
	if !strings.Contains(coloured.String(), "\x1b[") {
		t.Error("no escape codes with colour on")
	}
}
