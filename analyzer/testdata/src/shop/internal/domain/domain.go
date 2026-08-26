// Package domain depends on nothing but the export defaults. It must produce no
// diagnostics at all — the absence of `want` comments here is the assertion that matters
// most, because a linter that cries wolf on clean code does not stay installed.
package domain

import "time"

type Order struct {
	ID     string
	Placed time.Time
}

func New(id string) *Order { return &Order{ID: id, Placed: time.Now()} }
