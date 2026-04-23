package store

import (
	"codexswitch/internal/conf"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"codexswitch/internal/utils/log"
)

type accountTokens struct {
	AccountID    string `json:"account_id,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

type accountFile struct {
	Tokens      accountTokens `json:"tokens"`
	LastRefresh string        `json:"last_refresh,omitempty"`
}

type accountMeta struct {
	Email     string
	Plan      string
	AccountID string
}

type loadedAccount struct {
	Name          string
	FilePath      string
	Email         string
	Plan          string
	AccountID     string
	Authorization string
	LastRefresh   string
	TokenExpires  string
	HasRefresh    bool
	Auth          accountFile
}

type accountState struct {
	Current string `json:"current"`
}

type AccountView struct {
	Name           string         `json:"name"`
	Email          string         `json:"email"`
	Plan           string         `json:"plan"`
	Current        bool           `json:"current"`
	LastRefresh    string         `json:"lastRefresh,omitempty"`
	TokenExpiresAt string         `json:"tokenExpiresAt,omitempty"`
	Quota          *usageResponse `json:"quota,omitempty"`
}

type AccountStore struct {
	dir       string
	statePath string
	mu        sync.RWMutex
	actionMu  sync.Mutex
	current   string
	accounts  []loadedAccount
	loadErr   error
}

var Accounts = &AccountStore{
	dir:       conf.DataPath("accounts"),
	statePath: conf.DataPath("current-account.json"),
}

func (s *AccountStore) List() ([]AccountView, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.accounts) == 0 {
		if s.loadErr != nil {
			return nil, "", s.loadErr
		}
		return []AccountView{}, "", nil
	}

	items := make([]AccountView, 0, len(s.accounts))
	for _, account := range s.accounts {
		items = append(items, AccountView{
			Name:           account.Name,
			Email:          account.Email,
			Plan:           account.Plan,
			Current:        account.Name == s.current,
			LastRefresh:    account.LastRefresh,
			TokenExpiresAt: account.TokenExpires,
			Quota:          Quotas.Peek(account.quotaKey()),
		})
	}
	return items, s.current, nil
}

func (s *AccountStore) SelectByName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("account name is required")
	}

	s.actionMu.Lock()
	defer s.actionMu.Unlock()

	s.mu.RLock()
	if len(s.accounts) == 0 {
		err := s.loadErr
		s.mu.RUnlock()
		if err != nil {
			return err
		}
		return errors.New("no valid account files found in data/accounts directory")
	}
	found := false
	for _, account := range s.accounts {
		if account.Name == name {
			found = true
			break
		}
	}
	s.mu.RUnlock()
	if !found {
		return errors.New("account not found")
	}
	if err := s.writeCurrent(name); err != nil {
		return err
	}

	s.mu.Lock()
	s.current = name
	s.mu.Unlock()
	return nil
}

func (s *AccountStore) SelectNextByName(name string) (string, bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false, errors.New("account name is required")
	}

	s.actionMu.Lock()
	defer s.actionMu.Unlock()

	s.mu.RLock()
	if len(s.accounts) == 0 {
		err := s.loadErr
		s.mu.RUnlock()
		if err != nil {
			return "", false, err
		}
		return "", false, errors.New("no valid account files found in data/accounts directory")
	}
	if s.current != name {
		current := s.current
		s.mu.RUnlock()
		return current, false, nil
	}

	index := -1
	for i, account := range s.accounts {
		if account.Name == name {
			index = i
			break
		}
	}
	if index < 0 {
		s.mu.RUnlock()
		return "", false, errors.New("account not found")
	}

	next := ""
	nextRank := 0
	nextUsed := 0.0
	for _, account := range s.accounts {
		if account.Name == name {
			continue
		}
		rank, used := quotaRank(Quotas.Peek(account.quotaKey()))
		if next == "" || rank < nextRank || (rank == nextRank && (used < nextUsed || used == nextUsed && account.Name < next)) {
			next = account.Name
			nextRank = rank
			nextUsed = used
		}
	}
	s.mu.RUnlock()
	if next == "" {
		return name, false, nil
	}
	if err := s.writeCurrent(next); err != nil {
		return "", false, err
	}

	s.mu.Lock()
	s.current = next
	s.mu.Unlock()
	return next, true, nil
}

func (s *AccountStore) CurrentAuthorization() (string, string, error) {
	account, current, err := s.currentAccount()
	if err != nil {
		return "", "", err
	}
	if account.Authorization != "" && !account.isAccessTokenExpired() {
		return current, account.Authorization, nil
	}
	if !account.HasRefresh {
		return "", "", errors.New("current account access token is unavailable and refresh_token is missing")
	}
	if err := s.RefreshTokenByName(current); err != nil {
		return "", "", err
	}
	account, current, err = s.currentAccount()
	if err != nil {
		return "", "", err
	}
	if account.Authorization == "" {
		return "", "", errors.New("current account access token is unavailable")
	}
	return current, account.Authorization, nil
}

func (s *AccountStore) RefreshTokenByName(name string) error {
	s.actionMu.Lock()
	defer s.actionMu.Unlock()

	account, err := s.accountByName(name)
	if err != nil {
		return err
	}
	if strings.TrimSpace(account.Auth.Tokens.RefreshToken) == "" {
		return errors.New("account refresh_token is missing")
	}
	auth := account.Auth
	if err := refreshAccessToken(&auth); err != nil {
		return err
	}
	if _, err := s.updateAccountAuth(name, auth); err != nil {
		return err
	}
	return nil
}

func (s *AccountStore) DeleteByName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("account name is required")
	}

	s.actionMu.Lock()
	defer s.actionMu.Unlock()

	account, err := s.accountByName(name)
	if err != nil {
		return err
	}
	if err := os.Remove(account.FilePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := Quotas.Delete(account.quotaKey()); err != nil {
		return err
	}

	s.mu.Lock()
	items := make([]loadedAccount, 0, len(s.accounts))
	current := s.current
	for _, item := range s.accounts {
		if item.Name != name {
			items = append(items, item)
		}
	}
	s.accounts = items
	s.loadErr = nil
	if current == name {
		s.current = ""
	}
	if len(s.accounts) > 0 && s.current == "" {
		s.current = s.accounts[0].Name
	}
	current = s.current
	s.mu.Unlock()

	return s.writeCurrent(current)
}

func (s *AccountStore) Refresh() error {
	s.mu.RLock()
	current := s.current
	s.mu.RUnlock()
	if current == "" {
		current = s.readCurrent()
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		s.mu.Lock()
		s.accounts = nil
		s.current = ""
		s.loadErr = err
		s.mu.Unlock()
		return err
	}

	loaded := make([]loadedAccount, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			log.Warnf("failed to read account file %s: %v", entry.Name(), err)
			continue
		}
		var parsed accountFile
		if err := json.Unmarshal(content, &parsed); err != nil {
			log.Warnf("failed to parse account file %s: %v", entry.Name(), err)
			continue
		}
		account, err := newLoadedAccount(filepath.Join(s.dir, entry.Name()), strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())), parsed)
		if err != nil {
			log.Warnf("skip account file %s: %v", entry.Name(), err)
			continue
		}
		loaded = append(loaded, account)
	}
	if len(loaded) == 0 {
		err = errors.New("no valid account files found in data/accounts directory")
		s.mu.Lock()
		s.accounts = nil
		s.current = ""
		s.loadErr = err
		s.mu.Unlock()
		return err
	}
	sort.Slice(loaded, func(i int, j int) bool {
		if loaded[i].Name == current {
			return true
		}
		if loaded[j].Name == current {
			return false
		}
		return loaded[i].Name < loaded[j].Name
	})

	s.mu.Lock()
	s.accounts = loaded
	s.loadErr = nil
	s.current = loaded[0].Name
	for _, account := range s.accounts {
		if account.Name == current {
			s.current = current
			break
		}
	}
	selected := s.current
	s.mu.Unlock()

	if selected != "" && selected != current {
		return s.writeCurrent(selected)
	}
	return nil
}

func (s *AccountStore) currentAccount() (loadedAccount, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.accounts) == 0 {
		if s.loadErr != nil {
			return loadedAccount{}, "", s.loadErr
		}
		return loadedAccount{}, "", errors.New("no valid account files found in data/accounts directory")
	}
	for _, account := range s.accounts {
		if account.Name == s.current {
			return account, s.current, nil
		}
	}
	return loadedAccount{}, "", errors.New("account not found")
}

func (s *AccountStore) accountByName(name string) (loadedAccount, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.accounts) == 0 {
		if s.loadErr != nil {
			return loadedAccount{}, s.loadErr
		}
		return loadedAccount{}, errors.New("no valid account files found in data/accounts directory")
	}
	for _, account := range s.accounts {
		if account.Name == name {
			return account, nil
		}
	}
	return loadedAccount{}, errors.New("account not found")
}

func (s *AccountStore) updateAccountAuth(name string, auth accountFile) (loadedAccount, error) {
	account, err := s.accountByName(name)
	if err != nil {
		return loadedAccount{}, err
	}
	updated, err := newLoadedAccount(account.FilePath, account.Name, auth)
	if err != nil {
		return loadedAccount{}, err
	}
	if err := writeAccountFile(updated.FilePath, auth); err != nil {
		return loadedAccount{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.accounts {
		if s.accounts[i].Name != name {
			continue
		}
		s.accounts[i] = updated
		return updated, nil
	}
	return loadedAccount{}, errors.New("account not found")
}

func newLoadedAccount(filePath string, name string, auth accountFile) (loadedAccount, error) {
	if !hasAccountTokens(auth) {
		return loadedAccount{}, errors.New("token is empty")
	}

	meta := getAccountMeta(auth)
	email := meta.Email
	if email == "" {
		email = strings.TrimSpace(strings.ToLower(name))
	}

	account := loadedAccount{
		Name:        strings.TrimSpace(name),
		FilePath:    filePath,
		Email:       email,
		Plan:        meta.Plan,
		AccountID:   meta.AccountID,
		LastRefresh: strings.TrimSpace(auth.LastRefresh),
		HasRefresh:  strings.TrimSpace(auth.Tokens.RefreshToken) != "",
		Auth:        auth,
	}
	if accessToken := strings.TrimSpace(auth.Tokens.AccessToken); accessToken != "" {
		account.Authorization = "Bearer " + accessToken
	}
	if expiry := getTokenExpiry(auth); !expiry.IsZero() {
		account.TokenExpires = expiry.UTC().Format(time.RFC3339)
	}
	return account, nil
}

func (a loadedAccount) isAccessTokenExpired() bool {
	if a.TokenExpires == "" {
		return false
	}
	expiry, err := time.Parse(time.RFC3339, a.TokenExpires)
	if err != nil {
		return false
	}
	return !expiry.After(time.Now())
}

func (a loadedAccount) quotaKey() string {
	if a.Email != "" {
		return strings.ToLower(a.Email)
	}
	return strings.ToLower(a.Name)
}

func hasAccountTokens(auth accountFile) bool {
	return strings.TrimSpace(auth.Tokens.AccessToken) != "" ||
		strings.TrimSpace(auth.Tokens.RefreshToken) != "" ||
		strings.TrimSpace(auth.Tokens.IDToken) != ""
}

func decodeJWTPayload(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil
	}
	return parsed
}

func getAccountMeta(auth accountFile) accountMeta {
	payload := decodeJWTPayload(strings.TrimSpace(auth.Tokens.IDToken))
	authInfo, _ := payload["https://api.openai.com/auth"].(map[string]any)

	email, _ := payload["email"].(string)
	plan, _ := authInfo["chatgpt_plan_type"].(string)
	accountID := strings.TrimSpace(auth.Tokens.AccountID)
	if accountID == "" {
		if parsed, ok := authInfo["chatgpt_account_id"].(string); ok {
			accountID = parsed
		}
	}
	if strings.TrimSpace(plan) == "" {
		plan = "unknown"
	}
	return accountMeta{
		Email:     strings.TrimSpace(strings.ToLower(email)),
		Plan:      strings.TrimSpace(plan),
		AccountID: strings.TrimSpace(accountID),
	}
}

func getTokenExpiry(auth accountFile) time.Time {
	payload := decodeJWTPayload(strings.TrimSpace(auth.Tokens.AccessToken))
	exp, ok := payload["exp"].(float64)
	if !ok || exp <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(exp), 0)
}

func refreshAccessToken(auth *accountFile) error {
	if strings.TrimSpace(auth.Tokens.RefreshToken) == "" {
		return errors.New("no refresh_token in auth file")
	}

	body := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {auth.Tokens.RefreshToken},
		"client_id":     {openAIClientID},
	}.Encode()

	var response struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := requestJSON(tokenURL, "POST", map[string]string{
		"Content-Type":   "application/x-www-form-urlencoded",
		"Content-Length": strconv.Itoa(len(body)),
		"User-Agent":     outboundUserAgent,
	}, strings.NewReader(body), &response); err != nil {
		return err
	}

	if strings.TrimSpace(response.IDToken) != "" {
		auth.Tokens.IDToken = strings.TrimSpace(response.IDToken)
	}
	if strings.TrimSpace(response.AccessToken) != "" {
		auth.Tokens.AccessToken = strings.TrimSpace(response.AccessToken)
	}
	if strings.TrimSpace(response.RefreshToken) != "" {
		auth.Tokens.RefreshToken = strings.TrimSpace(response.RefreshToken)
	}
	auth.LastRefresh = time.Now().UTC().Format(time.RFC3339)
	return nil
}

func (s *AccountStore) readCurrent() string {
	if strings.TrimSpace(s.statePath) == "" {
		return ""
	}

	content, err := os.ReadFile(s.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		log.Warnf("failed to read current account state: %v", err)
		return ""
	}

	var state accountState
	if err := json.Unmarshal(content, &state); err != nil {
		log.Warnf("failed to parse current account state: %v", err)
		return ""
	}
	return strings.TrimSpace(state.Current)
}

func (s *AccountStore) writeCurrent(name string) error {
	if strings.TrimSpace(s.statePath) == "" {
		return nil
	}

	content, err := json.MarshalIndent(accountState{Current: strings.TrimSpace(name)}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.statePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.statePath, append(content, '\n'), 0o644)
}

func writeAccountFile(filePath string, auth accountFile) error {
	content, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filePath, append(content, '\n'), 0o644)
}
