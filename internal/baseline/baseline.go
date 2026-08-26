// Package baseline freezes the violations a codebase already has, so the rule can be
// switched on today and obeyed gradually.
//
// This is the difference between a tool people adopt and a tool people uninstall. Turning
// archlint on for the first time in a mature repository reports hundreds of findings, and
// nobody is going to fix them before their next commit lands. Without a way to say "not
// these, not yet", the rule never gets switched on at all.
package baseline

import (
	"bytes"
	"fmt"
	"os"
	"sort"

	"github.com/ddromanidis/arch-linter/internal/report"
	"gopkg.in/yaml.v3"
)

// Entry is one frozen class of violation.
//
// Deliberately not keyed on file and line. A baseline pinned to positions goes stale the
// moment anybody reformats, moves a function, or adds an import above — it would report
// "new" violations for code that has not changed, and then people stop believing it. The
// key is what the violation *is*: this component, breaking this rule, about this package.
type Entry struct {
	Component string `yaml:"component"`
	Rule      string `yaml:"rule"`
	Target    string `yaml:"target"`
	// Count is what makes it a ratchet rather than an amnesty. Existing violations are
	// forgiven; a tenth instance of something there were nine of is not.
	Count int `yaml:"count"`
}

func (e Entry) key() string { return e.Component + "\x00" + e.Rule + "\x00" + e.Target }

// File is the on-disk baseline.
type File struct {
	Version int     `yaml:"version"`
	Entries []Entry `yaml:"entries"`
}

const header = `# archlint baseline — written by ` + "`archlint baseline`" + `.
#
# Each entry freezes violations that already existed when the rule was switched on. A run
# fails when a count goes up, so the architecture can only improve: fix some, re-run
# ` + "`archlint baseline`" + `, and the numbers come down. Delete an entry once it reaches zero.
`

// Load reads a baseline. A missing file means nothing is forgiven, which is the right
// default: silence should have to be asked for.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &File{Version: 1}, nil
	}
	if err != nil {
		return nil, err
	}
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if f.Version != 1 {
		return nil, fmt.Errorf("%s: version must be 1, got %d", path, f.Version)
	}
	return &f, nil
}

// Save writes a baseline, sorted so that it diffs cleanly.
func Save(path string, findings []report.Finding) error {
	f := From(findings)
	body, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append([]byte(header), body...), 0o644)
}

// From tallies findings into a baseline.
func From(findings []report.Finding) *File {
	counts := map[string]*Entry{}
	for _, fd := range findings {
		e := Entry{Component: fd.Component, Rule: fd.Rule, Target: target(fd)}
		if existing, ok := counts[e.key()]; ok {
			existing.Count++
			continue
		}
		e.Count = 1
		counts[e.key()] = &e
	}

	out := &File{Version: 1}
	for _, e := range counts {
		out.Entries = append(out.Entries, *e)
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if a.Component != b.Component {
			return a.Component < b.Component
		}
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		return a.Target < b.Target
	})
	return out
}

// Apply removes findings the baseline forgives, and reports how many entries are now
// over-counted — that is, how much of the baseline is stale and could be tightened.
//
// Findings are dropped up to each entry's count. Which specific instances get forgiven is
// arbitrary, and has to be: the baseline deliberately does not know where they were.
func (f *File) Apply(findings []report.Finding) (kept []report.Finding, forgiven int) {
	budget := map[string]int{}
	for _, e := range f.Entries {
		budget[e.key()] = e.Count
	}

	// Sorted first, so that which instances are forgiven is at least deterministic across
	// runs even though it is arbitrary.
	report.Sort(findings)

	for _, fd := range findings {
		k := Entry{Component: fd.Component, Rule: fd.Rule, Target: target(fd)}.key()
		if budget[k] > 0 {
			budget[k]--
			forgiven++
			continue
		}
		kept = append(kept, fd)
	}
	return kept, forgiven
}

// Stale returns entries whose count exceeds what the code now produces, which is the
// prompt to re-run `archlint baseline` and lock the improvement in.
func (f *File) Stale(findings []report.Finding) []Entry {
	actual := map[string]int{}
	for _, fd := range findings {
		actual[Entry{Component: fd.Component, Rule: fd.Rule, Target: target(fd)}.key()]++
	}
	var out []Entry
	for _, e := range f.Entries {
		if actual[e.key()] < e.Count {
			out = append(out, e)
		}
	}
	return out
}

// target extracts the package a finding is about, which is the last stable part of its
// identity — the message wording may change between versions, the package will not.
func target(f report.Finding) string {
	if f.Target != "" {
		return f.Target
	}
	return f.Message
}
