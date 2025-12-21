package infrastructure

import (
	"example/internal/models"

	"gorm.io/gorm"
)

type GormRepo struct {
	db *gorm.DB
}

// NewGormRepo returns a concrete implementation
func NewGormRepo(db *gorm.DB) *GormRepo {
	return &GormRepo{db: db}
}

func (r *GormRepo) Save(u *models.User) error {
	return r.db.Create(u).Error
}

func (r *GormRepo) Find(id uint) (*models.User, error) {
	var u models.User
	if err := r.db.First(&u, id).Error; err != nil {
		return nil, err
	}
	return &u, nil
}
