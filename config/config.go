package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

type BrowserConfig struct {
	Headless    bool     `mapstructure:"headless"`
	UserAgents  []string `mapstructure:"user_agents"`
	MinViewport int      `mapstructure:"min_viewport"`
	MaxViewport int      `mapstructure:"max_viewport"`
	Bin         string   `mapstructure:"bin"`
}

type TimingConfig struct {
	MinDelayMs int `mapstructure:"min_delay_ms"`
	// MaxDelayMs controls the upper bound for human-like pacing between actions.
	MaxDelayMs int `mapstructure:"max_delay_ms"`
}

type LimitsConfig struct {
	DailyConnections int `mapstructure:"daily_connections"`
}

type LoggingConfig struct {
	Level string `mapstructure:"level"`
}

type Config struct {
	Browser BrowserConfig `mapstructure:"browser"`
	Timing  TimingConfig  `mapstructure:"timing"`
	Limits  LimitsConfig  `mapstructure:"limits"`
	Logging LoggingConfig `mapstructure:"logging"`
}

func Load(path string) (*Config, error) {
	setDefaults()

	v := viper.New()
	v.SetConfigFile(path)
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}

	return cfg, nil
}

func setDefaults() {
	viper.SetDefault("browser.headless", true)
	viper.SetDefault("browser.user_agents", []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15",
	})
	viper.SetDefault("browser.min_viewport", 1280)
	viper.SetDefault("browser.max_viewport", 1600)
	viper.SetDefault("browser.bin", "")

	viper.SetDefault("timing.min_delay_ms", 750)
	viper.SetDefault("timing.max_delay_ms", 2250)

	viper.SetDefault("limits.daily_connections", 50)

	viper.SetDefault("logging.level", "info")
}

func (c *Config) validate() error {
	if len(c.Browser.UserAgents) == 0 {
		return fmt.Errorf("browser.user_agents must include at least one value")
	}
	if c.Browser.MinViewport <= 0 {
		return fmt.Errorf("browser.min_viewport must be greater than zero")
	}
	if c.Browser.MaxViewport <= c.Browser.MinViewport {
		return fmt.Errorf("browser.max_viewport must be greater than min_viewport")
	}
	if c.Timing.MinDelayMs <= 0 || c.Timing.MaxDelayMs <= 0 {
		return fmt.Errorf("timing delays must be positive")
	}
	if c.Timing.MaxDelayMs < c.Timing.MinDelayMs {
		return fmt.Errorf("timing.max_delay_ms must be >= min_delay_ms")
	}
	if c.Limits.DailyConnections <= 0 {
		return fmt.Errorf("limits.daily_connections must be positive")
	}

	c.Logging.Level = strings.ToLower(c.Logging.Level)

	return nil
}

