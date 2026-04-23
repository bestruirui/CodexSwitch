package handlers

import (
	"net/http"
	"strings"

	"codexswitch/internal/conf"
	"codexswitch/internal/server/resp"
	"codexswitch/internal/server/router"

	"github.com/gin-gonic/gin"
)

type loginRequest struct {
	Password string `json:"password"`
}

func init() {
	router.NewGroupRouter("/api/auth").
		AddRoute(router.NewRoute("/login", http.MethodPost).Handle(login))
}

func login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if strings.TrimSpace(conf.AppConfig.Auth.Secret) == "" {
		resp.Error(c, http.StatusInternalServerError, "auth secret is not configured")
		return
	}
	if req.Password != conf.AppConfig.Auth.Secret {
		resp.Error(c, http.StatusUnauthorized, resp.ErrUnauthorized)
		return
	}
	resp.Success(c, gin.H{"authenticated": true})
}
