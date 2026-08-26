package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ddromanidis/arch-linter/config"
)

// Every preset must parse and validate. A scaffold that produces a file the tool rejects
// is worse than no scaffold: it is the first thing a new user sees, and it would be broken.
func TestEveryPresetIsValid(t *testing.T) {
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "arch.yaml")
			if err := Write(path, name, "example.test/proj"); err != nil {
				t.Fatalf("write: %v", err)
			}
			arch, err := config.ParseArch(path)
			if err != nil {
				t.Fatalf("the %s preset does not validate: %v", name, err)
			}
			if len(arch.Components) == 0 {
				t.Error("no components")
			}
			if arch.Module != "example.test/proj" {
				t.Errorf("module = %q, want the one from go.mod", arch.Module)
			}
			// And it must resolve, not merely parse.
			r := config.NewResolver(arch, "")
			for cname, c := range arch.Components {
				for _, pattern := range append([]string{c.Path}, c.Paths...) {
					if pattern == "" {
						continue
					}
					probe := "example.test/proj/" + strings.TrimSuffix(pattern, "/...")
					if got := r.Component(probe); got != cname {
						t.Errorf("%s resolves to %q, want %q", probe, got, cname)
					}
				}
			}
		})
	}
}

// The presets exist to teach the rule nobody has seen before, so the ones that model a real
// architecture must actually demonstrate it: a component that may import its driver and
// may not re-export it. Without that they are just directory lists.
func TestPresetsDemonstrateTheExportRule(t *testing.T) {
	for _, name := range []string{"layered", "hexagonal", "ddd"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "arch.yaml")
			if err := Write(path, name, "example.test/proj"); err != nil {
				t.Fatal(err)
			}
			arch, err := config.ParseArch(path)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, c := range arch.Components {
				imports := map[string]bool{}
				for _, i := range c.Imports {
					imports[i] = true
				}
				exports := map[string]bool{}
				for _, e := range c.Exports {
					exports[e] = true
				}
				if imports["gorm.io/gorm"] && !exports["gorm.io/gorm"] {
					found = true
				}
			}
			if !found {
				t.Errorf("the %s preset does not show a component importing a driver "+
					"it may not export, which is the whole point of the tool", name)
			}
		})
	}
}

func TestWriteRefusesToClobber(t *testing.T) {
	path := filepath.Join(t.TempDir(), "arch.yaml")
	if err := os.WriteFile(path, []byte("hand written\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, "minimal", ""); err == nil {
		t.Fatal("an existing arch.yaml is a hand-edited document and must not be overwritten")
	}
	body, _ := os.ReadFile(path)
	if string(body) != "hand written\n" {
		t.Error("the file was modified anyway")
	}
}

func TestUnknownPresetListsTheRealOnes(t *testing.T) {
	err := Write(filepath.Join(t.TempDir(), "arch.yaml"), "onion", "")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, name := range Names() {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the error should suggest %q: %v", name, err)
		}
	}
}
