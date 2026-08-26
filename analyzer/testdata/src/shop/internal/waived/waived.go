// Package waived exercises the //arch-lint:ignore directive.
//
// Waivers are what make the tool adoptable on code that already exists: a rule you cannot
// locally override is a rule people delete rather than obey. The cost of that power is
// that waivers rot, so the ones that explain nothing or suppress nothing are reported.
package waived

import (
	"shop/driver" //arch-lint:ignore imports the migration path genuinely needs the handle

	//arch-lint:ignore imports temporary, until the port lands
	"shop/internal/infra"

	"shop/internal/app" // want `waived may not import app \(shop/internal/app\)`
)

// A waiver on the line above covers a declaration whose signature spans lines, which is
// where a doc comment goes and therefore the only place it can go.
//
// arch-lint:ignore exports the installer is handed the raw handle on purpose
func Migrate(db *driver.DB) error { return nil }

// Unwaived, so still reported.
func Raw() *driver.DB { return nil } // want `Raw exposes shop/driver, which waived may not export`

// A waiver naming the wrong rule does not apply, and is then itself unused.
//
// arch-lint:ignore imports wrong rule // want `this imports waiver suppressed nothing`
func AlsoRaw() *driver.DB { return nil } // want `AlsoRaw exposes shop/driver, which waived may not export`

// This one suppresses nothing, because the line below breaks no rule.
//
// arch-lint:ignore exports stale // want `this exports waiver suppressed nothing`
func Fine() string { return "" }

var _ = infra.Open
var _ = app.Place
