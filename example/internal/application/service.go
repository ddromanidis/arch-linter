package application

import (
	"errors"

	"example/internal/models"
)

type UserService struct {
	repo models.UserRepository
}

// NewUserService depends on the models.UserRepository interface
func NewUserService(repo models.UserRepository) *UserService {
	return &UserService{repo: repo}
}

// Register returns a *models.User.
// This is allowed because 'application' allows exporting 'models'.
func (s *UserService) Register(name string) (*models.User, error) {
	if name == "" {
		return nil, errors.New("empty name")
	}
	return &models.User{Name: name}, nil
}
