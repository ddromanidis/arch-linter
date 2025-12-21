package main

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"example/internal/api"
	"example/internal/application"
	"example/internal/infrastructure"
)

func main() {
	// 1. Infrastructure
	var db *gorm.DB // placeholder
	repo := infrastructure.NewGormRepo(db)

	// 2. Application
	svc := application.NewUserService(repo)

	// 3. API
	handler := api.NewUserHandler(svc)

	// 4. Run
	r := gin.Default()
	r.POST("/users", handler.Create)
	r.Run(":8080")
}
