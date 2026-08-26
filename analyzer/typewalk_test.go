package analyzer

import (
	"go/types"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/packages"
)

// driverSrc stands in for gorm: a type, a constraint and a func type, which is enough to
// build every leak shape out of.
const driverSrc = `package driver

type DB struct{ Conn string }

type Model interface{ TableName() string }

type Option func(*DB)

type Tabler interface{ Table() string }
`

// leakySrc is the point of the whole tool. Every declaration here exposes package driver
// through the exported API, and not one of them says "driver" in a place that reading the
// syntax would find. v1's extractTypeName finds none of them.
const leakySrc = `package leaky

import "example.test/driver"

// 1. alias — a leak wearing a local name
type Repo = driver.DB

// 2. map value
func Get() map[string]*driver.DB { return nil }

// 3. generic constraint — driver appears nowhere but the constraint
func List[T driver.Model]() []T { return nil }

// 4. embedded field
type Handle struct{ *driver.DB }

// 5. variadic
func Configure(opts ...driver.Option) {}

// 6. interface method set
type Store interface{ Save(*driver.DB) error }

// 7. embedded interface
type Fancy interface{ driver.Tabler }

// 8. channel of a leaked type
func Stream() <-chan driver.DB { return nil }

// 9. generic instantiation of a local type
type Box[T any] struct{ V T }

func Boxed() Box[driver.DB] { return Box[driver.DB]{} }
`

// cleanSrc must produce no references to driver at all — the false-positive control. A leak
// detector that flags everything is as useless as one that flags nothing.
const cleanSrc = `package clean

import "example.test/driver"

// Unexported, so not API surface.
type hidden struct{ db *driver.DB }

// An unexported *named* field of an exported struct is not reachable.
type Service struct {
	Name string
	db   *driver.DB
}

// The driver is used internally and never mentioned in the signature.
func New(dsn string) *Service { _ = driver.DB{Conn: dsn}; return &Service{} }

func (s *Service) Do() (string, error) { return s.Name, nil }
`

// load type-checks a throwaway module and returns its packages by name.
func load(t *testing.T, files map[string]string) map[string]*packages.Package {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test\n\ngo 1.24\n")
	for rel, content := range files {
		write(rel, content)
	}

	cfg := &packages.Config{
		Dir:  dir,
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	out := map[string]*packages.Package{}
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			t.Fatalf("%s: %v", p.PkgPath, p.Errors)
		}
		out[p.Name] = p
	}
	return out
}

// exportedRefs maps each exported name in a package to the package paths its type reaches.
func exportedRefs(pkg *packages.Package) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	scope := pkg.Types.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}
		paths := map[string]bool{}
		for p := range referencedPackages(pkg.Types, obj.Type()) {
			paths[p.Path()] = true
		}
		// Methods hang off the type, not the package scope, so they need collecting too.
		if named, ok := obj.Type().(*types.Named); ok {
			for i := range named.NumMethods() {
				m := named.Method(i)
				if !m.Exported() {
					continue
				}
				for p := range referencedPackages(pkg.Types, m.Type()) {
					paths[p.Path()] = true
				}
			}
		}
		out[name] = paths
	}
	return out
}

// The headline test: every one of the nine shapes must be caught.
func TestTypewalkCatchesEveryLeakShape(t *testing.T) {
	pkgs := load(t, map[string]string{
		"driver/driver.go": driverSrc,
		"leaky/leaky.go":   leakySrc,
	})
	refs := exportedRefs(pkgs["leaky"])
	const driver = "example.test/driver"

	for _, name := range []string{
		"Repo",      // alias
		"Get",       // map value
		"List",      // generic constraint
		"Handle",    // embedded field
		"Configure", // variadic
		"Store",     // interface method set
		"Fancy",     // embedded interface
		"Stream",    // channel
		"Boxed",     // generic instantiation
	} {
		got, ok := refs[name]
		if !ok {
			t.Errorf("%s: not found in exported scope", name)
			continue
		}
		if !got[driver] {
			t.Errorf("%s: leak NOT detected — referenced %v, want it to include %s",
				name, keys(got), driver)
		}
	}
}

// The control: nothing in cleanSrc exposes the driver, so nothing may be reported.
func TestTypewalkDoesNotFlagInternalUse(t *testing.T) {
	pkgs := load(t, map[string]string{
		"driver/driver.go": driverSrc,
		"clean/clean.go":   cleanSrc,
	})
	refs := exportedRefs(pkgs["clean"])
	const driver = "example.test/driver"

	for name, paths := range refs {
		if paths[driver] {
			t.Errorf("%s: false positive — driver is used internally, not exposed", name)
		}
	}
	// Sanity: the walk is actually running and finding the exported names.
	if _, ok := refs["Service"]; !ok {
		t.Error("Service missing — the test is not exercising what it thinks")
	}
}

// A named type is a boundary: referring to it must not drag in what it is made of.
// Without this, exposing any domain type would transitively implicate that type's own
// dependencies and the linter would flag nearly all real code.
func TestTypewalkStopsAtNamedTypeBoundaries(t *testing.T) {
	pkgs := load(t, map[string]string{
		"driver/driver.go": driverSrc,
		"mid/mid.go": `package mid

import "example.test/driver"

type Wrapper struct{ db *driver.DB }
`,
		"top/top.go": `package top

import "example.test/mid"

func Make() *mid.Wrapper { return nil }
`,
	})
	refs := exportedRefs(pkgs["top"])
	got := refs["Make"]
	if !got["example.test/mid"] {
		t.Error("Make should reference mid")
	}
	if got["example.test/driver"] {
		t.Error("Make must NOT reach driver through mid.Wrapper's unexported field")
	}
}

// Cyclic types must not hang the walk.
func TestTypewalkTerminatesOnCycles(t *testing.T) {
	pkgs := load(t, map[string]string{
		"cyc/cyc.go": `package cyc

type Node struct {
	Next *Node
	Peer *Other
}

type Other struct{ Back *Node }
`,
	})
	// Completing at all is the assertion.
	refs := exportedRefs(pkgs["cyc"])
	if _, ok := refs["Node"]; !ok {
		t.Error("Node missing")
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
