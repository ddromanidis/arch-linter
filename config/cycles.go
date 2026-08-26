package config

import (
	"sort"
	"strings"
)

// Cycle is a dependency loop between components, as a path that returns to where it
// started: [app events support app].
type Cycle []string

// String renders a cycle the way it reads: app → events → support → app.
func (c Cycle) String() string { return strings.Join(c, " → ") }

// Cycles finds every dependency loop in the declared component graph.
//
// Go already refuses import cycles between *packages*, which is why nobody writes a
// checker for them. It cannot see a cycle between *components*, because a component is
// several packages: a/one.go importing b/one.go while b/two.go imports a/two.go compiles
// perfectly, and yet A and B are now mutually dependent and neither can be understood,
// tested or extracted without the other. That is precisely what layering exists to
// prevent, and the compiler is structurally incapable of noticing it.
//
// The allowlists make it hard to do by accident — you have to write `A: imports: [B]` and
// `B: imports: [A]`, and a reviewer might catch that. Across a dozen components a loop
// three hops long is much easier to add than to spot, which is what this is for.
//
// Checked against the *declared* graph rather than the real imports, because the declared
// graph is a superset: nothing can import what it has not declared, so a config without
// cycles guarantees code without them. That also means this needs no code and no build —
// it is an answer about arch.yaml alone.
func (a *Arch) Cycles() []Cycle {
	// Sorted throughout, so the same file always reports the same cycles in the same
	// order. A checker that reported them in map order would produce a different failure
	// message on every run.
	names := make([]string, 0, len(a.Components))
	for name := range a.Components {
		names = append(names, name)
	}
	sort.Strings(names)

	adj := make(map[string][]string, len(names))
	for _, name := range names {
		c := a.Components[name]
		deps := map[string]bool{}
		// Imports and exports both, because exporting a component implies importing it.
		for _, rule := range append(append([]string{}, c.Imports...), c.Exports...) {
			if rule != name {
				if _, isComponent := a.Components[rule]; isComponent {
					deps[rule] = true
				}
			}
		}
		list := make([]string, 0, len(deps))
		for d := range deps {
			list = append(list, d)
		}
		sort.Strings(list)
		adj[name] = list
	}

	const (
		white = iota // not visited
		grey         // on the current path
		black        // finished
	)
	colour := make(map[string]int, len(names))

	var path []string
	var found []Cycle
	seen := map[string]bool{}

	var visit func(string)
	visit = func(n string) {
		colour[n] = grey
		path = append(path, n)
		for _, m := range adj[n] {
			switch colour[m] {
			case white:
				visit(m)
			case grey:
				// A back edge to something still on the path closes a loop. The cycle is
				// the tail of the path from m onwards, plus m again to show the return.
				for i, p := range path {
					if p == m {
						cyc := append(append(Cycle{}, path[i:]...), m)
						if key := canonical(cyc); !seen[key] {
							seen[key] = true
							found = append(found, cyc)
						}
						break
					}
				}
			}
		}
		path = path[:len(path)-1]
		colour[n] = black
	}

	for _, n := range names {
		if colour[n] == white {
			visit(n)
		}
	}
	return found
}

// canonical keys a cycle by its members regardless of where the walk happened to enter it.
// A → B → A and B → A → B are one cycle reported twice otherwise, and which one you get
// depends on nothing more meaningful than alphabetical order.
func canonical(c Cycle) string {
	if len(c) < 2 {
		return strings.Join(c, "\x00")
	}
	ring := c[:len(c)-1] // drop the repeated node
	at := 0
	for i, s := range ring {
		if s < ring[at] {
			at = i
		}
	}
	rotated := make([]string, 0, len(ring))
	for i := range ring {
		rotated = append(rotated, ring[(at+i)%len(ring)])
	}
	return strings.Join(rotated, "\x00")
}
