package api

import (
	"net/http"

	"example/internal/application"

	"github.com/gin-gonic/gin"
)

type UserHandler struct {
	svc *application.UserService
}

func NewUserHandler(svc *application.UserService) *UserHandler {
	return &UserHandler{svc: svc}
}

// Create handles the HTTP request.
// It uses gin.Context (allowed via imports) but does not return it (so no export check needed).
func (h *UserHandler) Create(c *gin.Context) {
	u, err := h.svc.Register("Alice")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, u)
}
