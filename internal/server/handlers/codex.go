package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httputil"
	"strings"

	"codexswitch/internal/server/middleware"
	"codexswitch/internal/server/resp"
	"codexswitch/internal/server/router"
	"codexswitch/internal/store"
	"codexswitch/internal/utils/log"
	"codexswitch/internal/xhttp"

	"github.com/gin-gonic/gin"
)

type codexAccountNameContextKey struct{}

var errAbortUpstream429 = errors.New("abort client response after upstream 429")

var codexProxy = &httputil.ReverseProxy{
	Transport: xhttp.Transport(),
	Rewrite: func(pr *httputil.ProxyRequest) {
		pr.Out.URL.Scheme = "https"
		pr.Out.URL.Host = "chatgpt.com"
		pr.Out.Host = "chatgpt.com"
		pr.Out.URL.Path = "/backend-api/codex" + strings.TrimPrefix(pr.In.URL.Path, "/api/codex")
		pr.Out.URL.RawPath = "/backend-api/codex" + strings.TrimPrefix(pr.In.URL.EscapedPath(), "/api/codex")
	},
	ModifyResponse: func(response *http.Response) error {
		if response.StatusCode != http.StatusTooManyRequests {
			return nil
		}
		if response.Request == nil {
			return nil
		}

		accountName, _ := response.Request.Context().Value(codexAccountNameContextKey{}).(string)
		if strings.TrimSpace(accountName) == "" {
			return nil
		}

		next, switched, err := store.Accounts.SelectNextByName(accountName)
		if err != nil {
			log.Warnf("failed to switch account after upstream 429 for %s: %v", accountName, err)
			return nil
		}
		go func(name string) {
			if _, err := store.Quotas.RefreshByName(name); err != nil {
				log.Warnf("background quota refresh failed after upstream 429 for %s: %v", name, err)
			}
		}(accountName)
		if switched {
			log.Warnf("upstream returned 429 for %s, switched current account to %s", accountName, next)
		}
		return errAbortUpstream429
	},
	ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
		if errors.Is(err, errAbortUpstream429) {
			panic(http.ErrAbortHandler)
		}
		http.Error(w, "proxy upstream unavailable", http.StatusBadGateway)
	},
}

func init() {
	proxyRouter := router.NewGroupRouter("/api")
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions} {
		proxyRouter.Use(middleware.ApiAuth()).
			AddRoute(router.NewRoute("/codex", method).Handle(proxyCodex)).
			AddRoute(router.NewRoute("/codex/*path", method).Handle(proxyCodex))
	}
}

func proxyCodex(c *gin.Context) {
	accountName, authorization, err := store.Accounts.CurrentAuthorization()
	if err != nil {
		resp.Error(c, http.StatusServiceUnavailable, err.Error())
		return
	}
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), codexAccountNameContextKey{}, accountName))
	c.Request.Header.Set("Authorization", authorization)
	codexProxy.ServeHTTP(c.Writer, c.Request)
}
