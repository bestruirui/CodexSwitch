package task

import (
	"time"

	"codexswitch/internal/store"
	"codexswitch/internal/utils/log"
)

func Run(stop <-chan struct{}) {
	refreshMissingQuotaOnStartup(stop)

	timer := time.NewTimer(time.Minute)
	defer timer.Stop()

	nextTokenRefresh := time.Now().Add(time.Minute)
	refreshedQuota := make(map[string]int64)
	for {
		select {
		case <-stop:
			return
		case <-timer.C:
		}

		now := time.Now()
		if !nextTokenRefresh.After(now) {
			refreshExpiringTokens(stop, now)
			nextTokenRefresh = now.Add(time.Hour)
		}
		refreshUsedQuotaOnReset(stop, now, refreshedQuota)
		timer.Reset(time.Minute)
	}
}

func refreshMissingQuotaOnStartup(stop <-chan struct{}) {
	items, _, err := store.Accounts.List()
	if err != nil {
		log.Warnf("startup quota refresh skipped: %v", err)
		return
	}
	for _, account := range items {
		select {
		case <-stop:
			return
		default:
		}
		if account.Quota != nil {
			continue
		}
		if _, err := store.Quotas.RefreshByName(account.Name); err != nil {
			log.Warnf("startup quota refresh failed for %s: %v", account.Name, err)
		}
	}
}

func refreshExpiringTokens(stop <-chan struct{}, now time.Time) {
	items, _, err := store.Accounts.List()
	if err != nil {
		log.Warnf("scheduled token refresh skipped: %v", err)
		return
	}
	for _, account := range items {
		select {
		case <-stop:
			return
		default:
		}
		if account.TokenExpiresAt == "" {
			continue
		}
		expiry, err := time.Parse(time.RFC3339, account.TokenExpiresAt)
		if err != nil || expiry.After(now.Add(7*24*time.Hour)) {
			continue
		}
		if err := store.Accounts.RefreshTokenByName(account.Name); err != nil {
			log.Warnf("scheduled token refresh failed for %s: %v", account.Name, err)
		}
	}
}

func refreshUsedQuotaOnReset(stop <-chan struct{}, now time.Time, refreshed map[string]int64) {
	due, err := store.Quotas.DueUsedQuotaResets(now)
	if err != nil {
		log.Warnf("scheduled quota refresh skipped: %v", err)
		return
	}
	for name, resetAt := range refreshed {
		if due[name] != resetAt {
			delete(refreshed, name)
		}
	}
	for name, resetAt := range due {
		select {
		case <-stop:
			return
		default:
		}
		if refreshed[name] == resetAt {
			continue
		}
		if _, err := store.Quotas.RefreshByName(name); err != nil {
			log.Warnf("scheduled quota refresh failed for %s: %v", name, err)
			continue
		}
		refreshed[name] = resetAt
	}
}
