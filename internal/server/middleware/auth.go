package middleware

import (
	"net/http"
	"strings"

	"codexswitch/internal/conf"
	"codexswitch/internal/server/resp"

	"github.com/gin-gonic/gin"
)

func Auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.TrimSpace(c.GetHeader("X-Proxy-Password")) != conf.AppConfig.Auth.Secret {
			resp.Error(c, http.StatusUnauthorized, resp.ErrUnauthorized)
			c.Abort()
			return
		}
		c.Next()
	}
}
func ApiAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.TrimSpace(c.GetHeader("Authorization")) != "Bearer "+conf.AppConfig.Auth.ApiKey {
			resp.Error(c, http.StatusUnauthorized, resp.ErrUnauthorized)
			c.Abort()
			return
		}
		c.Next()
	}
}
