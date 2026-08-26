package config

import (
	"os"
	"strings"
	"testing"
)

const sample = `
version: 1
module: github.com/acme/shop
components:
  domain:
    path: internal/domain/...
    imports: []
    exports: []
  app:
    path: internal/app/...
    imports: [domain]
    exports: [domain]
  infra:
    path: internal/infra/...
    imports: [domain, app, gorm.io/gorm]
    exports: [domain, app]
  api:
    paths:
      - internal/api/...
      - internal/transport/http/...
    imports: [app, domain, net/http]
    exports: [app, domain]
defaults:
  imports: [fmt, errors, context]
  exports: [time, context]
`

func resolver(t *testing.T) *Resolver {
	t.Helper()
	a, err := parseArch([]byte(sample), "arch.yaml")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return NewResolver(a, "")
}

func TestComponentResolution(t *testing.T) {
	r := resolver(t)
	for _, tc := range []struct{ path, want string }{
		{"github.com/acme/shop/internal/domain", "domain"},
		{"github.com/acme/shop/internal/domain/order", "domain"},
		{"github.com/acme/shop/internal/app", "app"},
		{"github.com/acme/shop/internal/api/v1", "api"},
		// The second pattern of a multi-path component resolves the same as the first.
		{"github.com/acme/shop/internal/transport/http", "api"},
		// Outside the architecture entirely.
		{"gorm.io/gorm", ""},
		{"fmt", ""},
		{"github.com/acme/shop/internal/unclassified", ""},
		// A prefix that only *looks* like a match must not be one: `domainer` is not
		// under `domain`, and string prefixes alone would say it was.
		{"github.com/acme/shop/internal/domainer", ""},
	} {
		if got := r.Component(tc.path); got != tc.want {
			t.Errorf("Component(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestImportRules(t *testing.T) {
	r := resolver(t)
	for _, tc := range []struct {
		from, target string
		want         bool
		why          string
	}{
		{"app", "github.com/acme/shop/internal/domain", true, "declared"},
		{"domain", "github.com/acme/shop/internal/app", false, "domain depends on nothing"},
		{"domain", "gorm.io/gorm", false, "the leak that matters"},
		{"infra", "gorm.io/gorm", true, "infra owns the driver"},
		{"api", "net/http", true, "exact stdlib path"},
		{"domain", "fmt", true, "defaults apply to everyone"},
		{"domain", "context", true, "defaults apply to everyone"},
		{"domain", "net/http", false, "not in defaults, not declared"},
		// A component always reaches itself, however it is split across packages.
		{"domain", "github.com/acme/shop/internal/domain/order", true, "same component"},
		// An undeclared component permits nothing, rather than everything.
		{"nonexistent", "fmt", true, "defaults still apply"},
		{"nonexistent", "gorm.io/gorm", false, "no rules means no permission"},
	} {
		if got := r.AllowsImport(tc.from, tc.target); got != tc.want {
			t.Errorf("AllowsImport(%q, %q) = %v, want %v (%s)",
				tc.from, tc.target, got, tc.want, tc.why)
		}
	}
}

// The point of the whole tool: infra may import gorm but may not expose it.
func TestExportRulesDifferFromImportRules(t *testing.T) {
	r := resolver(t)
	if !r.AllowsImport("infra", "gorm.io/gorm") {
		t.Error("infra should be allowed to import gorm")
	}
	if r.AllowsExport("infra", "gorm.io/gorm") {
		t.Error("infra must not be allowed to expose gorm in its API surface")
	}
	if !r.AllowsExport("infra", "github.com/acme/shop/internal/domain") {
		t.Error("infra should be allowed to expose domain types")
	}
	if !r.AllowsExport("domain", "time") {
		t.Error("time is in the export defaults")
	}
}

func TestPrefixRuleMatching(t *testing.T) {
	a, err := parseArch([]byte(`
version: 1
module: github.com/acme/shop
components:
  app:
    path: internal/app/...
    imports: ["github.com/acme/vendorlib/..."]
`), "arch.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r := NewResolver(a, "")
	for _, tc := range []struct {
		target string
		want   bool
	}{
		{"github.com/acme/vendorlib", true},
		{"github.com/acme/vendorlib/deep/nested", true},
		{"github.com/acme/vendorlibrary", false}, // not a path boundary
		{"github.com/acme/other", false},
	} {
		if got := r.AllowsImport("app", tc.target); got != tc.want {
			t.Errorf("AllowsImport(app, %q) = %v, want %v", tc.target, got, tc.want)
		}
	}
}

// The module path is implicit in every pattern, so a component declared relative to it
// must resolve the same as one spelled out in full.
func TestModuleOverrideFromGoMod(t *testing.T) {
	a, err := parseArch([]byte(`
version: 1
components:
  app:
    path: internal/app/...
`), "arch.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r := NewResolver(a, "github.com/from/gomod")
	if got := r.Component("github.com/from/gomod/internal/app/x"); got != "app" {
		t.Errorf("Component = %q, want app", got)
	}
}

func TestArchValidation(t *testing.T) {
	for _, tc := range []struct{ name, yaml, wantErr string }{
		{
			// The v1 bug, now impossible: a key the parser does not know is an error.
			"unknown key",
			"version: 1\nglobal:\n  allow: [fmt]\ncomponents:\n  a:\n    path: a",
			"field global not found",
		},
		{"wrong version", "version: 2\ncomponents:\n  a:\n    path: a", "version must be 1"},
		{"no components", "version: 1\ncomponents: {}", "no components"},
		{"missing path", "version: 1\ncomponents:\n  a:\n    imports: []", `has no path`},
		{
			"rule naming a component that does not exist",
			"version: 1\ncomponents:\n  a:\n    path: a\n    imports: [typo]",
			"neither a component nor an import path",
		},
		{
			"midway ellipsis",
			"version: 1\ncomponents:\n  a:\n    path: internal/.../x",
			"only meaningful as a trailing",
		},
		{
			"empty rule entry",
			"version: 1\ncomponents:\n  a:\n    path: a\n    imports: [\"\"]",
			"empty entry",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseArch([]byte(tc.yaml), "arch.yaml")
			if err == nil {
				t.Fatalf("expected an error containing %q, got none", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error was %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestArchAcceptsValidRules(t *testing.T) {
	// A component name, a stdlib path, a module path and a prefix pattern are all legal.
	_, err := parseArch([]byte(`
version: 1
components:
  a:
    path: a
    imports: [b, net/http, gorm.io/gorm, "github.com/x/y/..."]
  b:
    path: b
`), "arch.yaml")
	if err != nil {
		t.Fatalf("expected these to be accepted: %v", err)
	}
}

func TestConfigDefaultsAndValidation(t *testing.T) {
	c := Default()
	if c.Output.Format != "text" || c.Severity.Exports != Error {
		t.Errorf("unexpected defaults: %+v", c)
	}
	if c.IncludeTests {
		t.Error("tests should be excluded by default")
	}

	for _, tc := range []struct{ name, yaml, wantErr string }{
		{"bad format", "version: 1\noutput:\n  format: xml", "unknown output format"},
		{"bad severity", "version: 1\nseverity:\n  imports: loud", "severity.imports"},
		{"bad colour", "version: 1\noutput:\n  color: rainbow", "color must be"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseConfig([]byte(tc.yaml), "arch.config.yaml")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("got %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

// Decoding over the defaults must not blank the fields the file does not mention.
func TestConfigPartialFileKeepsDefaults(t *testing.T) {
	c, err := parseConfig([]byte("version: 1\noutput:\n  format: json\n"), "arch.config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if c.Output.Format != "json" {
		t.Errorf("format = %q, want json", c.Output.Format)
	}
	if c.Output.Color != "auto" {
		t.Errorf("color = %q, want the default auto", c.Output.Color)
	}
	if c.Severity.Exports != Error {
		t.Errorf("exports severity = %q, want the default error", c.Severity.Exports)
	}
}

func TestModulePathFromGoMod(t *testing.T) {
	dir := t.TempDir()
	f := dir + "/go.mod"
	if err := os.WriteFile(f, []byte("module github.com/acme/shop\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ModulePath(f)
	if err != nil {
		t.Fatal(err)
	}
	if got != "github.com/acme/shop" {
		t.Errorf("ModulePath = %q", got)
	}
}

// Exposing a type requires importing it, so an export permission has to carry an import
// permission with it — otherwise the export rule could never be exercised. The implication
// runs one way only: importing the driver must not entitle infra to hand it out.
func TestExportPermissionImpliesImportPermission(t *testing.T) {
	a, err := parseArch([]byte(`
version: 1
module: github.com/acme/shop
components:
  domain:
    path: internal/domain/...
defaults:
  exports: [time]
`), "arch.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r := NewResolver(a, "")
	if !r.AllowsExport("domain", "time") {
		t.Fatal("time is in the export defaults")
	}
	if !r.AllowsImport("domain", "time") {
		t.Error("a package that may be exposed must be importable")
	}

	// And the converse must not hold.
	r2 := resolver(t)
	if !r2.AllowsImport("infra", "gorm.io/gorm") {
		t.Fatal("infra imports gorm")
	}
	if r2.AllowsExport("infra", "gorm.io/gorm") {
		t.Error("importing gorm must not entitle infra to export it")
	}
}

// `std` allows the whole standard library in one entry. Enumerating it is possible and
// pointless: the first real repository this ran against produced hundreds of findings for
// regexp, io/fs, math and cmp, which buries the rules worth having. No architecture is
// damaged by a package using the standard library.
func TestStdKeyword(t *testing.T) {
	a, err := parseArch([]byte(`
version: 1
module: github.com/acme/shop
components:
  app:
    path: internal/app/...
    imports: [std]
`), "arch.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r := NewResolver(a, "")
	for _, p := range []string{"fmt", "io/fs", "net/http", "regexp", "unicode/utf8", "database/sql"} {
		if !r.AllowsImport("app", p) {
			t.Errorf("std should allow %q", p)
		}
	}
	// It is the standard library, not everything.
	for _, p := range []string{"gorm.io/gorm", "github.com/x/y", "github.com/acme/shop/internal/infra"} {
		if r.AllowsImport("app", p) {
			t.Errorf("std must not allow %q", p)
		}
	}
}

// The standard library is decided by Go's own rule — a module path's first element contains
// a dot — not by a list that goes stale. This was a hardcoded list, and Go 1.27 added
// `uuid` to the standard library, so a real codebase importing it was told the standard
// library was an undeclared third-party dependency.
func TestStdlibDetectionDoesNotGoStale(t *testing.T) {
	for _, p := range []string{
		"fmt", "net/http", "io/fs", "unicode/utf8", "log/slog", "database/sql",
		"uuid",              // added to the standard library in Go 1.27
		"somefuturepackage", // and whatever is added next
	} {
		if !isStdlib(p) {
			t.Errorf("isStdlib(%q) = false, want true", p)
		}
	}
	for _, p := range []string{
		"github.com/x/y", "gorm.io/gorm", "golang.org/x/tools", "entgo.io/ent",
	} {
		if isStdlib(p) {
			t.Errorf("isStdlib(%q) = true, want false", p)
		}
	}
}
