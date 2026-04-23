package handlers

import (
	"net/http"

	"codexswitch/internal/server/middleware"
	"codexswitch/internal/server/resp"
	"codexswitch/internal/server/router"
	"codexswitch/internal/store"

	"github.com/gin-gonic/gin"
)

type selectAccountRequest struct {
	Name string `json:"name"`
}

type actionReport struct {
	Succeeded []string `json:"succeeded,omitempty"`
	Failed    []string `json:"failed,omitempty"`
}

func init() {
	router.NewGroupRouter("/api/accounts").
		Use(middleware.Auth()).
		AddRoute(router.NewRoute("", http.MethodGet).Handle(listAccounts)).
		AddRoute(router.NewRoute("/refresh", http.MethodPost).Handle(refreshAccounts)).
		AddRoute(router.NewRoute("/refresh-token", http.MethodPost).Handle(refreshAllTokens)).
		AddRoute(router.NewRoute("/refresh-quota", http.MethodPost).Handle(refreshAllQuota)).
		AddRoute(router.NewRoute("/select", http.MethodPost).Handle(selectAccount))

	router.NewGroupRouter("/api/accounts/:name").
		Use(middleware.Auth()).
		AddRoute(router.NewRoute("", http.MethodDelete).Handle(deleteAccount)).
		AddRoute(router.NewRoute("/refresh-token", http.MethodPost).Handle(refreshToken)).
		AddRoute(router.NewRoute("/refresh-quota", http.MethodPost).Handle(refreshQuota))
}

func listAccounts(c *gin.Context) {
	respondAccounts(c, nil)
}

func refreshAccounts(c *gin.Context) {
	if err := store.Accounts.Refresh(); err != nil {
		resp.Error(c, http.StatusServiceUnavailable, err.Error())
		return
	}
	if err := store.Quotas.Load(); err != nil {
		resp.Error(c, http.StatusServiceUnavailable, err.Error())
		return
	}
	if err := store.Quotas.SyncWithAccounts(); err != nil {
		resp.Error(c, http.StatusServiceUnavailable, err.Error())
		return
	}
	respondAccounts(c, gin.H{"message": "账号与配额缓存已从目录重新加载。"})
}

func selectAccount(c *gin.Context) {
	var req selectAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if err := store.Accounts.SelectByName(req.Name); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	respondAccounts(c, gin.H{"message": "账号已切换。"})
}

func deleteAccount(c *gin.Context) {
	name := c.Param("name")
	if err := store.Accounts.DeleteByName(name); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	respondAccounts(c, gin.H{"message": "账号已删除。"})
}

func refreshToken(c *gin.Context) {
	name := c.Param("name")
	if err := store.Accounts.RefreshTokenByName(name); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	respondAccounts(c, gin.H{"message": "Token 已刷新。"})
}

func refreshQuota(c *gin.Context) {
	name := c.Param("name")
	if _, err := store.Quotas.RefreshByName(name); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	respondAccounts(c, gin.H{"message": "Quota 已刷新。"})
}

func refreshAllTokens(c *gin.Context) {
	items, _, err := store.Accounts.List()
	if err != nil {
		resp.Error(c, http.StatusServiceUnavailable, err.Error())
		return
	}

	report := actionReport{
		Succeeded: make([]string, 0, len(items)),
		Failed:    make([]string, 0),
	}
	for _, account := range items {
		if err := store.Accounts.RefreshTokenByName(account.Name); err != nil {
			report.Failed = append(report.Failed, account.Name+": "+err.Error())
			continue
		}
		report.Succeeded = append(report.Succeeded, account.Name)
	}
	respondAccounts(c, gin.H{"message": "全量 Token 刷新已完成。", "report": report})
}

func refreshAllQuota(c *gin.Context) {
	items, _, err := store.Accounts.List()
	if err != nil {
		resp.Error(c, http.StatusServiceUnavailable, err.Error())
		return
	}

	report := actionReport{
		Succeeded: make([]string, 0, len(items)),
		Failed:    make([]string, 0),
	}
	for _, account := range items {
		if _, err := store.Quotas.RefreshByName(account.Name); err != nil {
			report.Failed = append(report.Failed, account.Name+": "+err.Error())
			continue
		}
		report.Succeeded = append(report.Succeeded, account.Name)
	}
	respondAccounts(c, gin.H{"message": "全量 Quota 刷新已完成。", "report": report})
}

func respondAccounts(c *gin.Context, extra gin.H) {
	items, current, err := store.Accounts.List()
	if err != nil {
		resp.Error(c, http.StatusServiceUnavailable, err.Error())
		return
	}

	data := gin.H{
		"accounts": items,
		"current":  current,
	}
	for key, value := range extra {
		data[key] = value
	}
	resp.Success(c, data)
}
