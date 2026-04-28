package store

import (
	"codexswitch/internal/conf"
	"codexswitch/internal/utils/cache"
	"codexswitch/internal/xhttp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	openAIClientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
	tokenURL          = "https://auth.openai.com/oauth/token"
	usageURL          = "https://chatgpt.com/backend-api/wham/usage"
	outboundUserAgent = "codex-tui/0.122.0 (Windows 10.0.19044; x86_64) vscode/1.111.0 (codex-tui; 0.122.0)"
)

type usageWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

type usageResponse struct {
	UserID    string `json:"user_id"`
	AccountID string `json:"account_id"`
	Email     string `json:"email"`
	PlanType  string `json:"plan_type"`
	RateLimit struct {
		Allowed         bool         `json:"allowed"`
		LimitReached    bool         `json:"limit_reached"`
		PrimaryWindow   *usageWindow `json:"primary_window"`
		SecondaryWindow *usageWindow `json:"secondary_window"`
	} `json:"rate_limit"`
	AdditionalRateLimits []struct {
		LimitName string `json:"limit_name"`
		RateLimit struct {
			Allowed         bool         `json:"allowed"`
			LimitReached    bool         `json:"limit_reached"`
			PrimaryWindow   *usageWindow `json:"primary_window"`
			SecondaryWindow *usageWindow `json:"secondary_window"`
		} `json:"rate_limit"`
	} `json:"additional_rate_limits"`
	CodeReviewRateLimit *struct {
		PrimaryWindow *usageWindow `json:"primary_window"`
	} `json:"code_review_rate_limit"`
	Credits struct {
		HasCredits          bool `json:"has_credits"`
		Unlimited           bool `json:"unlimited"`
		OverageLimitReached bool `json:"overage_limit_reached"`
		Balance             any  `json:"balance"`
		ApproxLocalMessages any  `json:"approx_local_messages"`
		ApproxCloudMessages any  `json:"approx_cloud_messages"`
	} `json:"credits"`
	SpendControl struct {
		Reached bool `json:"reached"`
	} `json:"spend_control"`
	RateLimitReachedType any `json:"rate_limit_reached_type"`
	Promo                *struct {
		CampaignID string `json:"campaign_id"`
		Message    string `json:"message"`
	} `json:"promo"`
	ReferralBeacon any `json:"referral_beacon"`
}

type QuotaStore struct {
	dir   string
	items cache.Cache[string, usageResponse]
}

type requestError struct {
	StatusCode int
	Body       string
}

var Quotas = &QuotaStore{
	dir:   conf.DataPath("quota"),
	items: cache.New[string, usageResponse](256),
}

var outboundHTTPClient = xhttp.NewClient(15 * time.Second)

func (e requestError) Error() string {
	if strings.TrimSpace(e.Body) == "" {
		return fmt.Sprintf("request failed with status %d", e.StatusCode)
	}
	return fmt.Sprintf("request failed with status %d: %s", e.StatusCode, e.Body)
}

func (s *QuotaStore) Load() error {
	if _, err := os.Stat(s.dir); errors.Is(err, os.ErrNotExist) {
		s.items.Clear()
		return nil
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}

	loaded := make(map[string]usageResponse, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}

		content, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			continue
		}

		var usage usageResponse
		if err := json.Unmarshal(content, &usage); err != nil {
			continue
		}

		key := strings.TrimSpace(strings.ToLower(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))))
		if key == "" {
			continue
		}
		loaded[key] = usage
	}

	s.items.Clear()
	for key, usage := range loaded {
		s.items.Set(key, usage)
	}
	return nil
}

func (s *QuotaStore) Peek(key string) *usageResponse {
	usage, ok := s.items.Get(normalizeQuotaKey(key))
	if !ok {
		return nil
	}
	cloned := usage
	return &cloned
}

func (s *QuotaStore) DueUsedQuotaResets(now time.Time) (map[string]int64, error) {
	items, _, err := Accounts.List()
	if err != nil {
		return nil, err
	}

	due := make(map[string]int64)
	for _, account := range items {
		resetAt := quotaAutoRefreshResetAt(account.Quota)
		if resetAt == 0 || resetAt > now.Unix() {
			continue
		}
		due[account.Name] = resetAt
	}
	return due, nil
}

func (s *QuotaStore) Delete(key string) error {
	key = normalizeQuotaKey(key)
	if key == "" {
		return nil
	}

	s.items.Del(key)
	if err := os.Remove(filepath.Join(s.dir, key+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *QuotaStore) SyncWithAccounts() error {
	valid := make(map[string]struct{})
	Accounts.mu.RLock()
	for _, account := range Accounts.accounts {
		valid[account.quotaKey()] = struct{}{}
	}
	Accounts.mu.RUnlock()

	for key := range s.items.GetAll() {
		if _, ok := valid[key]; !ok {
			s.items.Del(key)
		}
	}

	if _, err := os.Stat(s.dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}
		key := normalizeQuotaKey(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))
		if _, ok := valid[key]; ok {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *QuotaStore) RefreshByName(name string) (*usageResponse, error) {
	Accounts.actionMu.Lock()
	defer Accounts.actionMu.Unlock()

	account, err := Accounts.accountByName(name)
	if err != nil {
		return nil, err
	}

	auth := account.Auth
	usage, err := fetchUsage(&auth, account.AccountID)
	if err != nil {
		return nil, err
	}
	auth.LastRefresh = time.Now().UTC().Format(time.RFC3339)
	updatedAccount, err := Accounts.updateAccountAuth(name, auth)
	if err != nil {
		return nil, err
	}
	if err := s.store(updatedAccount.quotaKey(), usage); err != nil {
		return nil, err
	}
	return &usage, nil
}

func (s *QuotaStore) store(key string, usage usageResponse) error {
	key = normalizeQuotaKey(key)
	if key == "" {
		return errors.New("quota key is empty")
	}

	s.items.Set(key, usage)
	return writeQuotaCacheFile(filepath.Join(s.dir, key+".json"), usage)
}

func fetchUsage(auth *accountFile, accountID string) (usageResponse, error) {
	if strings.TrimSpace(auth.Tokens.AccessToken) == "" && strings.TrimSpace(auth.Tokens.RefreshToken) != "" {
		if err := refreshAccessToken(auth); err != nil {
			return usageResponse{}, err
		}
	}
	if expiry := getTokenExpiry(*auth); !expiry.IsZero() && !expiry.After(time.Now()) && strings.TrimSpace(auth.Tokens.RefreshToken) != "" {
		if err := refreshAccessToken(auth); err != nil {
			return usageResponse{}, err
		}
	}
	if strings.TrimSpace(auth.Tokens.AccessToken) == "" {
		return usageResponse{}, errors.New("no access_token in auth file")
	}

	headers := map[string]string{
		"Authorization": "Bearer " + strings.TrimSpace(auth.Tokens.AccessToken),
		"Accept":        "application/json",
		"User-Agent":    outboundUserAgent,
	}
	if id := firstNonEmpty(strings.TrimSpace(auth.Tokens.AccountID), strings.TrimSpace(accountID), getAccountMeta(*auth).AccountID); id != "" {
		headers["chatgpt-account-id"] = id
	}

	var response usageResponse
	if err := requestJSON(usageURL, http.MethodGet, headers, nil, &response); err != nil {
		var httpErr requestError
		if errors.As(err, &httpErr) && (httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden) && strings.TrimSpace(auth.Tokens.RefreshToken) != "" {
			if refreshErr := refreshAccessToken(auth); refreshErr != nil {
				return usageResponse{}, refreshErr
			}
			headers["Authorization"] = "Bearer " + strings.TrimSpace(auth.Tokens.AccessToken)
			if err := requestJSON(usageURL, http.MethodGet, headers, nil, &response); err != nil {
				return usageResponse{}, err
			}
			return response, nil
		}
		return usageResponse{}, err
	}
	return response, nil
}

func requestJSON(target string, method string, headers map[string]string, body io.Reader, out any) error {
	request, err := http.NewRequest(method, target, body)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", outboundUserAgent)
	for key, value := range headers {
		request.Header.Set(key, value)
	}

	response, err := outboundHTTPClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	content, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return requestError{
			StatusCode: response.StatusCode,
			Body:       strings.TrimSpace(string(content)),
		}
	}
	if out == nil || len(content) == 0 {
		return nil
	}
	return json.Unmarshal(content, out)
}

func normalizeQuotaKey(key string) string {
	return strings.TrimSpace(strings.ToLower(key))
}

func quotaRank(usage *usageResponse) (int, float64) {
	if usage == nil {
		return 1, 0
	}
	used, ok := quotaUsedPercent(usage)
	if quotaLimitReached(usage) {
		if !ok {
			return 2, 0
		}
		return 2, used
	}
	if !ok {
		return 1, 0
	}
	return 0, used
}

func quotaUsedPercent(usage *usageResponse) (float64, bool) {
	if usage == nil {
		return 0, false
	}

	used := 0.0
	found := false
	collect := func(window *usageWindow) {
		if window == nil {
			return
		}
		if !found || window.UsedPercent > used {
			used = window.UsedPercent
			found = true
		}
	}

	collect(usage.RateLimit.PrimaryWindow)
	collect(usage.RateLimit.SecondaryWindow)
	for _, item := range usage.AdditionalRateLimits {
		collect(item.RateLimit.PrimaryWindow)
		collect(item.RateLimit.SecondaryWindow)
	}
	if usage.CodeReviewRateLimit != nil {
		collect(usage.CodeReviewRateLimit.PrimaryWindow)
	}
	return used, found
}

func quotaLimitReached(usage *usageResponse) bool {
	if usage == nil {
		return false
	}
	if !usage.RateLimit.Allowed || usage.RateLimit.LimitReached || usage.SpendControl.Reached || usage.Credits.OverageLimitReached {
		return true
	}
	for _, item := range usage.AdditionalRateLimits {
		if !item.RateLimit.Allowed || item.RateLimit.LimitReached {
			return true
		}
	}
	return false
}

func quotaAutoRefreshResetAt(usage *usageResponse) int64 {
	if usage == nil {
		return 0
	}

	resetAt := int64(0)
	collect := func(window *usageWindow) {
		if window == nil || window.UsedPercent <= 0 || window.ResetAt <= 0 {
			return
		}
		if resetAt == 0 || window.ResetAt < resetAt {
			resetAt = window.ResetAt
		}
	}

	collect(usage.RateLimit.PrimaryWindow)
	collect(usage.RateLimit.SecondaryWindow)
	for _, item := range usage.AdditionalRateLimits {
		collect(item.RateLimit.PrimaryWindow)
		collect(item.RateLimit.SecondaryWindow)
	}
	if usage.CodeReviewRateLimit != nil {
		collect(usage.CodeReviewRateLimit.PrimaryWindow)
	}
	return resetAt
}

func writeQuotaCacheFile(filePath string, usage usageResponse) error {
	content, err := json.MarshalIndent(usage, "", "  ")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filePath, append(content, '\n'), 0o644)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
