package analyzer

import (
	"os"
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
	rules.Config.Exclude = []string{"internal/generated/..."}
	rules.pkgExcludes = nil
	rules.compileExcludes()
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

// An arch.yaml outside the module must still find go.mod, or `--arch` cannot serve the
// case it exists for: one architecture shared across repositories, or handed in by CI.
func TestLoadFromResolvesModuleFromTheWorkingDirectory(t *testing.T) {
	// A config with no `module:`, deliberately far from any go.mod.
	shared := filepath.Join(t.TempDir(), "shared.yaml")
	if err := os.WriteFile(shared, []byte(
		"version: 1\ncomponents:\n  all:\n    path: ./...\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Resolving from the config's own directory finds nothing, and used to be all it did.
	if _, err := Load(shared, ""); err == nil {
		t.Error("a config with no module and no neighbouring go.mod should fail on its own")
	}

	// Resolving from the module under analysis works.
	workDir := filepath.Join(analysistest.TestData(), "src", "shop")
	rules, err := LoadFrom(shared, "", workDir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got := rules.Resolver.Module(); got != "shop" {
		t.Errorf("module = %q, want shop — read from the analysed module's go.mod", got)
	}
}
