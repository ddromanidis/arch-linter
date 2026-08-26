// Package app may reach domain and nothing else. This is the ordinary import rule that
// every architecture linter has; it is here so the two rules are known not to interfere.
package app

import (
	"fmt"

	"shop/driver"          // want `app may not import shop/driver`
	"shop/internal/domain" // fine: declared
	"shop/internal/infra"  // want `app may not import infra \(shop/internal/infra\)`
)

var _ = fmt.Sprint // fmt is in the defaults, so importing it is fine

func Place(id string) *domain.Order { return domain.New(id) }

// An import that is allowed may still not be exposed — but app declares domain in both
// its imports and its exports, so this is clean.
func Lookup(r *infra.Repo, id string) *domain.Order { // want `Lookup exposes infra \(shop/internal/infra\), which app may not export`
	return domain.New(id)
}

var _ = driver.DB{}
