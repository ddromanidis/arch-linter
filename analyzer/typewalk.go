package analyzer

import "go/types"

// refs collects every package a type transitively refers to.
//
// This is the whole reason arch-lint exists. Import rules can be read off a file's import
// block, which is why every other Go architecture linter stops there. Export rules cannot:
// asking "does this exported signature mention a package it should not" means asking what
// the types in it actually *are*, and that question has no syntactic answer. `Repo` tells
// you nothing; `type Repo = gorm.DB` tells you everything, and only the type checker knows
// the difference.
//
// So this walks resolved types rather than syntax. The shapes below are not hypothetical —
// each one is a way a database handle has escaped a repository in real code:
//
//	type Repo = gorm.DB              // alias
//	func Get() map[string]*gorm.DB   // map value
//	func List[T gorm.Model]() []T    // generic constraint
//	type H struct{ *gorm.DB }        // embedded
//	func F(...gorm.Option)           // variadic
type refs struct {
	pkgs map[*types.Package]bool
	// own is the package being analysed, which decides how deep a named type is followed.
	// See the *types.Named arm.
	own *types.Package
	// seen guards the recursion. Go types are routinely cyclic — a linked-list node
	// contains a pointer to itself, and any two types that reference each other close a
	// loop — so an unguarded walk does not terminate.
	seen map[types.Type]bool
}

func newRefs(own *types.Package) *refs {
	return &refs{pkgs: map[*types.Package]bool{}, own: own, seen: map[types.Type]bool{}}
}

// packages returns the collected set.
func (r *refs) packages() map[*types.Package]bool { return r.pkgs }

// walk adds every package reachable from t.
func (r *refs) walk(t types.Type) {
	if t == nil || r.seen[t] {
		return
	}
	r.seen[t] = true

	switch t := t.(type) {
	// An alias is a second name for another type. Resolving through it is the single most
	// important case here: `type Repo = gorm.DB` is a leak wearing a local name, and it is
	// invisible to anything that reads syntax. Go 1.22 introduced types.Alias and 1.23
	// made it the default, so this arm is reached rather than silently skipped.
	case *types.Alias:
		// The alias name itself lives somewhere, and that somewhere counts: re-exporting
		// gorm.DB under your own name still puts gorm in your callers' hands.
		r.add(types.Unalias(t))
		r.walk(types.Unalias(t))

	case *types.Named:
		r.add(t)
		// Generic instantiations carry their arguments: Result[gorm.DB] leaks gorm even
		// though Result is local.
		if args := t.TypeArgs(); args != nil {
			for i := range args.Len() {
				r.walk(args.At(i))
			}
		}
		// Whether to look inside depends on whose type it is, and this is the distinction
		// the whole check turns on.
		//
		// Somebody else's named type is opaque. Exposing `order.Order` refers to package
		// order, and what order.Order is made of is order's business — descending would
		// make every exported type transitively answerable for its dependencies' internals
		// and flag nearly all real code.
		//
		// Your own named type is not a reference at all: it is a definition, and its
		// contents *are* your API surface. `type Handle struct{ *driver.DB }` mentions
		// driver nowhere a caller can see except in the struct that callers receive, so
		// stopping at the boundary here would miss the leak entirely.
		if obj := t.Obj(); obj != nil && obj.Pkg() == r.own {
			r.walk(t.Underlying())
		}

	case *types.Pointer:
		r.walk(t.Elem())
	case *types.Slice:
		r.walk(t.Elem())
	case *types.Array:
		r.walk(t.Elem())
	case *types.Chan:
		r.walk(t.Elem())
	case *types.Map:
		// Both halves. A map keyed by a domain ID with a driver handle for a value leaks
		// through the value, which a walk that only looked at Elem would still catch — but
		// the reverse arrangement is just as real.
		r.walk(t.Key())
		r.walk(t.Elem())

	case *types.Signature:
		// Type parameters first: their constraints are part of the signature's surface.
		// `func List[T gorm.Model]() []T` names gorm nowhere except in a constraint.
		if tp := t.TypeParams(); tp != nil {
			for i := range tp.Len() {
				r.walk(tp.At(i).Constraint())
			}
		}
		r.walk(t.Params())
		r.walk(t.Results())
		if recv := t.Recv(); recv != nil {
			r.walk(recv.Type())
		}

	case *types.Tuple:
		// Covers parameter and result lists, and with them variadic parameters, which are
		// an ordinary slice by the time the type checker is done with them.
		for i := range t.Len() {
			r.walk(t.At(i).Type())
		}

	case *types.Struct:
		for i := range t.NumFields() {
			f := t.Field(i)
			// Unexported fields are not part of the API surface, so they are not a leak —
			// with one exception. An embedded field promotes its methods into the struct's
			// own method set whether or not its name is exported, so an embedded
			// unexported type is still reachable through the value.
			if !f.Exported() && !f.Embedded() {
				continue
			}
			r.walk(f.Type())
		}

	case *types.Interface:
		for i := range t.NumExplicitMethods() {
			r.walk(t.ExplicitMethod(i).Type())
		}
		// Embedded constraints and interfaces: `interface{ gorm.Tabler }` mentions gorm
		// only here.
		for i := range t.NumEmbeddeds() {
			r.walk(t.EmbeddedType(i))
		}

	case *types.Union:
		// Constraint unions: `interface{ gorm.DB | sql.DB }`.
		for i := range t.Len() {
			r.walk(t.Term(i).Type())
		}

	case *types.TypeParam:
		r.walk(t.Constraint())

	case *types.Basic:
		// int, string and friends belong to no package.
	}
}

// add records the package a named or aliased type belongs to.
func (r *refs) add(t types.Type) {
	var obj *types.TypeName
	switch t := t.(type) {
	case *types.Named:
		obj = t.Obj()
	case *types.Alias:
		obj = t.Obj()
	default:
		return
	}
	if obj == nil {
		return
	}
	// Universe scope: error, any, comparable. No package, nothing to attribute.
	if pkg := obj.Pkg(); pkg != nil {
		r.pkgs[pkg] = true
	}
}

// referencedPackages is the entry point: every package that t exposes, as seen from own.
//
// own matters because a package's own type definitions are part of its surface while other
// packages' types are opaque references. See the *types.Named arm of walk.
func referencedPackages(own *types.Package, t types.Type) map[*types.Package]bool {
	r := newRefs(own)
	r.walk(t)
	return r.packages()
}
