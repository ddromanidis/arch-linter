// Package infra is allowed to import the driver and forbidden to hand it out. Every
// diagnostic below is an abstraction leak that an import-only linter reports as clean,
// because the import itself is legitimate.
package infra

import (
	"shop/driver"
	"shop/internal/domain"
)

// Legitimate: the driver is used, never exposed.
type Repo struct {
	db *driver.DB
}

func Open(dsn string) *Repo { return &Repo{db: &driver.DB{Conn: dsn}} }

func (r *Repo) Find(id string) *domain.Order { return domain.New(id) }

// 1. Alias — a leak wearing a local name.
type Handle = driver.DB // want `Handle exposes shop/driver, which infra may not export`

// 2. Map value.
func Pool() map[string]*driver.DB { return nil } // want `Pool exposes shop/driver, which infra may not export`

// 3. Generic constraint: the driver is named nowhere else in this signature.
func All[T driver.Model]() []T { return nil } // want `All exposes shop/driver, which infra may not export`

// 4. Embedded field, promoting the driver's method set into an exported struct.
type Wrapped struct { // want `Wrapped exposes shop/driver, which infra may not export`
	*driver.DB
}

// 5. Variadic.
func Configure(opts ...driver.Option) {} // want `Configure exposes shop/driver, which infra may not export`

// 6. Interface method set.
type Store interface { // want `Store exposes shop/driver, which infra may not export`
	Save(*driver.DB) error
}

// 7. Channel element.
func Stream() <-chan driver.DB { return nil } // want `Stream exposes shop/driver, which infra may not export`

// 8. An exported method on an exported type.
func (r *Repo) Raw() *driver.DB { return r.db } // want `Repo.Raw exposes shop/driver, which infra may not export`

// 9. Exported struct field.
type Config struct { // want `Config exposes shop/driver, which infra may not export`
	DB   *driver.DB
	Name string
}

// A const whose type comes from elsewhere. The const itself is a leak and is reported
// once. Its type's *methods* belong to driver, not to infra, so they must not be walked —
// doing so produced a second diagnostic pointing into the module cache, at a line in
// somebody else's source that nobody here can act on.
const Mode = driver.ModeFast // want `Mode exposes shop/driver, which infra may not export`
