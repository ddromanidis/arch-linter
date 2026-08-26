// Package diagram renders an arch.yaml as a Mermaid graph.
//
// The point is that arch.yaml is a design document that happens to be executable. A
// diagram generated from the same file the linter enforces cannot drift from the code the
// way a hand-drawn one in a wiki always does — and the arrows are true because a build
// fails when they stop being true.
package diagram

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/ddromanidis/arch-linter/config"
)

// Mermaid writes a flowchart of the component graph.
func Mermaid(w io.Writer, a *config.Arch) error {
	names := make([]string, 0, len(a.Components))
	for name := range a.Components {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("graph TD\n")

	for _, name := range names {
		c := a.Components[name]
		label := name
		if c.Description != "" {
			label = name + "<br/><i>" + escape(firstSentence(c.Description)) + "</i>"
		}
		fmt.Fprintf(&b, "  %s[%q]\n", id(name), label)
	}
	b.WriteString("\n")

	// Edges between components. External packages are deliberately left out: a real
	// arch.yaml permits a dozen third-party modules per component, and drawing them turns
	// the shape of the architecture — which is the only thing worth looking at — into a
	// hairball.
	for _, from := range names {
		c := a.Components[from]
		exports := set(c.Exports)
		for _, to := range sorted(c.Imports) {
			if _, ok := a.Components[to]; !ok {
				continue
			}
			// The asymmetry this tool exists to express: a component that may import
			// another but not re-expose it is holding that dependency privately. Drawn
			// dotted, because it is a weaker edge — it stops at the boundary.
			if exports[to] {
				fmt.Fprintf(&b, "  %s --> %s\n", id(from), id(to))
			} else {
				fmt.Fprintf(&b, "  %s -.->|uses, does not expose| %s\n", id(from), id(to))
			}
		}
	}

	_, err := io.WriteString(w, b.String())
	return err
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func set(in []string) map[string]bool {
	m := make(map[string]bool, len(in))
	for _, s := range in {
		m[s] = true
	}
	return m
}

// id makes a Mermaid-safe node identifier. Component names are free text and may contain
// hyphens, slashes or spaces, none of which Mermaid accepts unquoted.
func id(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return "c_" + b.String()
}

func escape(s string) string {
	return strings.NewReplacer(`"`, "'", "\n", " ").Replace(s)
}

// firstSentence keeps labels to a readable width. A description is prose written for the
// config file, where there is room; a node in a graph is not.
func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i > 0 {
		s = s[:i]
	}
	s = strings.TrimSuffix(s, ".")
	const max = 48
	if len(s) > max {
		s = strings.TrimSpace(s[:max]) + "…"
	}
	return s
}
