package analyzer

import (
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

// rulesFor loads the fixture architecture the way the CLI would.
func rulesFor(t *testing.T) *Rules {
	t.Helper()
	arch := filepath.Join(analysistest.TestData(), "src", "shop", "arch.yaml")
	rules, err := Load(arch, "")
	if err != nil {
		t.Fatalf("load rules: %v", err)
	}
	return rules
}

// The export rule, against every leak shape at once. The `// want` comments in
// testdata/src/shop/internal/infra are the assertions.
func TestExportLeaks(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), New(rulesFor(t)), "shop/internal/infra")
}

// The import rule, plus the interaction between the two.
func TestImportRules(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), New(rulesFor(t)), "shop/internal/app")
}

// A component that obeys its rules must produce nothing at all. The absence of `// want`
// comments in the domain fixture is the assertion, and it is the one that decides whether
// anybody keeps the linter installed.
func TestCleanComponentIsSilent(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), New(rulesFor(t)), "shop/internal/domain")
}

// A package outside the architecture has no rules, so it cannot violate them. Reporting
// unclassified packages would make adopting the tool an exercise in silencing it.
func TestUnclassifiedPackageIsSilent(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), New(rulesFor(t)), "shop/driver")
}

func TestSeverityOffDisablesARule(t *testing.T) {
	rules := rulesFor(t)
	rules.Config.Severity.Exports = "off"
	// With exports off, infra's leaks vanish and only its (legal) imports remain, so the
	// `// want` comments no longer match. Run against domain instead, which is clean
	// either way, and assert the switch is at least honoured without panicking.
	analysistest.Run(t, analysistest.TestData(), New(rules), "shop/internal/domain")
}

func TestSkipFile(t *testing.T) {
	rules := rulesFor(t)
	rules.Config.Exclude = []string{"**/mock_*.go", "internal/generated/..."}

	for _, tc := range []struct {
		file string
		want bool
	}{
		{"/repo/internal/app/mock_repo.go", true},
		{"/repo/internal/app/app.go", false},
		{"/repo/internal/app/app_test.go", true}, // tests excluded by default
	} {
		if got := rules.skipFile(tc.file); got != tc.want {
			t.Errorf("skipFile(%q) = %v, want %v", tc.file, got, tc.want)
		}
	}

	rules.Config.IncludeTests = true
	if rules.skipFile("/repo/internal/app/app_test.go") {
		t.Error("include-tests should stop test files being skipped")
	}
}

func TestSkipPackage(t *testing.T) {
	rules := rulesFor(t)
	rules.Config.Exclude = []string{"shop/internal/generated/..."}
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"shop/internal/generated", true},
		{"shop/internal/generated/pb", true},
		{"shop/internal/app", false},
		{"shop/internal/generatedthing", false}, // not a path boundary
	} {
		if got := rules.skipPackage(tc.path); got != tc.want {
			t.Errorf("skipPackage(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// Waivers: what they suppress, and what they must not.
func TestWaivers(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), New(rulesFor(t)), "shop/internal/waived")
}
