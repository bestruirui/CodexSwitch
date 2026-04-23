package cmd

import (
	"fmt"
	"net/http"
	"time"

	"codexswitch/internal/conf"
	_ "codexswitch/internal/server/handlers"
	"codexswitch/internal/server/middleware"
	"codexswitch/internal/server/router"
	"codexswitch/internal/store"
	"codexswitch/internal/utils/log"
	"codexswitch/internal/utils/shutdown"
	"codexswitch/static"

	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
)

var cfgFile string

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start " + conf.APP_NAME,
	PreRun: func(cmd *cobra.Command, args []string) {
		conf.PrintBanner()
		conf.Load(cfgFile)
	},
	Run: func(cmd *cobra.Command, args []string) {
		sd := shutdown.New(log.Logger)
		defer sd.Listen()
		stop := make(chan struct{})
		sd.Register(func() error {
			close(stop)
			return nil
		})

		if err := store.Accounts.Refresh(); err != nil {
			log.Warnf("initial account refresh failed: %v", err)
		}
		if err := store.Quotas.Load(); err != nil {
			log.Warnf("initial quota load failed: %v", err)
		}
		if err := store.Quotas.SyncWithAccounts(); err != nil {
			log.Warnf("initial quota cleanup failed: %v", err)
		}
		go refreshMissingQuotaOnStartup(stop)
		go refreshExpiringTokens(stop)

		if conf.IsDebug() {
			log.Infof("%s run at debug mode", conf.APP_NAME)
			gin.SetMode(gin.DebugMode)
		} else {
			log.Infof("%s run at release mode", conf.APP_NAME)
			gin.SetMode(gin.ReleaseMode)
		}

		r := gin.New()

		r.Use(middleware.Cors())
		if conf.IsDebug() {
			r.Use(middleware.Logger())
		}
		r.Use(middleware.StaticEmbed("/", static.Files))

		router.RegisterAll(r)

		httpSrv := &http.Server{Addr: fmt.Sprintf("%s:%d", conf.AppConfig.Server.Host, conf.AppConfig.Server.Port), Handler: r}
		go func() {
			if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Errorf("http server listen and serve error: %v", err)
			}
		}()
		sd.Register(httpSrv.Close)
	},
}

func init() {
	startCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is ./data/config.json)")
	rootCmd.AddCommand(startCmd)
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

func refreshExpiringTokens(stop <-chan struct{}) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()

	for {
		select {
		case <-stop:
			return
		case <-timer.C:
		}

		items, _, err := store.Accounts.List()
		if err != nil {
			log.Warnf("scheduled token refresh skipped: %v", err)
			timer.Reset(time.Hour)
			continue
		}
		for _, account := range items {
			if account.TokenExpiresAt == "" {
				continue
			}
			expiry, err := time.Parse(time.RFC3339, account.TokenExpiresAt)
			if err != nil || expiry.After(time.Now().Add(7*24*time.Hour)) {
				continue
			}
			if err := store.Accounts.RefreshTokenByName(account.Name); err != nil {
				log.Warnf("scheduled token refresh failed for %s: %v", account.Name, err)
			}
		}
		timer.Reset(time.Hour)
	}
}
