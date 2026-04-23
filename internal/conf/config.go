package conf

import (
	"fmt"
	"os"

	"codexswitch/internal/utils/log"

	"github.com/spf13/viper"
)

type Server struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type Log struct {
	Level string
}

type Auth struct {
	Secret string
	ApiKey string
}

type Proxy struct {
	Enabled  bool
	Type     string
	Host     string
	Port     int
	Username string
	Password string
}

type Config struct {
	Server Server
	Log    Log
	Auth   Auth
	Proxy  Proxy
}

var AppConfig Config

func Load(path string) error {
	if path != "" {
		viper.SetConfigFile(path)
	} else {
		viper.SetConfigName("config")
		viper.SetConfigType("json")
		viper.AddConfigPath(DataPath())
	}

	viper.AutomaticEnv()
	viper.SetEnvPrefix(APP_NAME)

	setDefaults()

	if err := viper.ReadInConfig(); err == nil {
		log.Infof("Using config file: %s", viper.ConfigFileUsed())
	} else {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			log.Infof("Config file not found, creating default config")
			if err := os.MkdirAll(DataPath(), 0755); err != nil {
				log.Errorf("Failed to create data directory: %v", err)
			}
			if err := viper.SafeWriteConfigAs(DataPath("config.json")); err != nil {
				log.Errorf("Failed to create default config: %v", err)
			}
		} else {
			return fmt.Errorf("error reading config file: %w", err)
		}
	}

	if err := viper.Unmarshal(&AppConfig); err != nil {
		return fmt.Errorf("unable to decode config into struct: %w", err)
	}
	return nil
}

func setDefaults() {
	viper.SetDefault("server.host", "127.0.0.1")
	viper.SetDefault("server.port", 8080)
	viper.SetDefault("log.level", "info")
	viper.SetDefault("auth.secret", "")
	viper.SetDefault("auth.api_key", "")
	viper.SetDefault("proxy.enabled", false)
	viper.SetDefault("proxy.type", "http")
	viper.SetDefault("proxy.host", "")
	viper.SetDefault("proxy.port", 0)
	viper.SetDefault("proxy.username", "")
	viper.SetDefault("proxy.password", "")
}
