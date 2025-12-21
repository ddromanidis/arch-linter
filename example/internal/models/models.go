package models

import "time"

// User is a pure domain entity.
type User struct {
	ID        uint
	Name      string
	Email     string
	CreatedAt time.Time
}

// UserRepository interface defines behavior but has no implementation dependencies.
type UserRepository interface {
	Save(u *User) error
	Find(id uint) (*User, error)
}
