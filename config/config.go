package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// ConfigPath stores the path to the config file for saving
var ConfigPath string

type Config struct {
	DataDir    string           `toml:"data_dir"` // session store directory, default ~/.cx-connect
	Agent      AgentConfig     `toml:"agent"`    // global agent config
	Platforms  []PlatformConfig `toml:"platforms"` // platform configs
	AllowUsers []AllowUser     `toml:"allow_users"`
	Log        LogConfig       `toml:"log"`
	Language   string          `toml:"language"` // "en" or "zh", default is "en"
	Speech     SpeechConfig    `toml:"speech"`
}

// SpeechConfig configures speech-to-text for voice messages.
type SpeechConfig struct {
	Enabled  bool   `toml:"enabled"`
	Provider string `toml:"provider"` // "openai" | "groq"
	Language string `toml:"language"` // e.g. "zh", "en"; empty = auto-detect
	OpenAI   struct {
		APIKey  string `toml:"api_key"`
		BaseURL string `toml:"base_url"`
		Model   string `toml:"model"`
	} `toml:"openai"`
	Groq struct {
		APIKey string `toml:"api_key"`
		Model  string `toml:"model"`
	} `toml:"groq"`
}

// AllowUser defines a user that is allowed to access the bot.
type AllowUser struct {
	Platform string `toml:"platform"` // "feishu", "telegram", "qq", etc.
	UserID   string `toml:"user_id"` // platform-specific user ID
}

type AgentConfig struct {
	Type      string           `toml:"type"`
	Options   map[string]any   `toml:"options"`
	Providers []ProviderConfig `toml:"providers"`
}

type ProviderConfig struct {
	Name    string            `toml:"name"`
	APIKey  string            `toml:"api_key"`
	BaseURL string            `toml:"base_url,omitempty"`
	Model   string            `toml:"model,omitempty"`
	Env     map[string]string `toml:"env,omitempty"`
}

type PlatformConfig struct {
	Type    string         `toml:"type"`
	Options map[string]any `toml:"options"`
}

type LogConfig struct {
	Level string `toml:"level"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	cfg := &Config{
		Log: LogConfig{Level: "info"},
	}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if cfg.DataDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cfg.DataDir = filepath.Join(home, ".cx-connect")
		} else {
			cfg.DataDir = ".cx-connect"
		}
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.Agent.Type == "" {
		return fmt.Errorf("config: [agent].type is required")
	}
	if len(c.Platforms) == 0 {
		return fmt.Errorf("config: at least one [[platforms]] is required")
	}
	for j, p := range c.Platforms {
		if p.Type == "" {
			return fmt.Errorf("config: platforms[%d].type is required", j)
		}
	}
	return nil
}

// SaveActiveProvider persists the active provider name.
func SaveActiveProvider(providerName string) error {
	if ConfigPath == "" {
		return fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	if cfg.Agent.Options == nil {
		cfg.Agent.Options = make(map[string]any)
	}
	cfg.Agent.Options["provider"] = providerName
	return saveConfig(cfg)
}

// AddProviderToConfig adds a provider to the agent config and saves.
func AddProviderToConfig(provider ProviderConfig) error {
	if ConfigPath == "" {
		return fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	for _, existing := range cfg.Agent.Providers {
		if existing.Name == provider.Name {
			return fmt.Errorf("provider %q already exists", provider.Name)
		}
	}
	cfg.Agent.Providers = append(cfg.Agent.Providers, provider)
	return saveConfig(cfg)
}

// RemoveProviderFromConfig removes a provider from the agent config and saves.
func RemoveProviderFromConfig(providerName string) error {
	if ConfigPath == "" {
		return fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	providers := cfg.Agent.Providers
	for j := range providers {
		if providers[j].Name == providerName {
			cfg.Agent.Providers = append(providers[:j], providers[j+1:]...)
			return saveConfig(cfg)
		}
	}
	return fmt.Errorf("provider %q not found", providerName)
}

func saveConfig(cfg *Config) error {
	f, err := os.Create(ConfigPath)
	if err != nil {
		return fmt.Errorf("create config: %w", err)
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(cfg)
}

// SaveLanguage saves the language setting to the config file.
func SaveLanguage(lang string) error {
	if ConfigPath == "" {
		return fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	cfg.Language = lang
	return saveConfig(cfg)
}

// GetAgentProviders returns providers for the agent.
func GetAgentProviders() ([]ProviderConfig, string, error) {
	if ConfigPath == "" {
		return nil, "", fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return nil, "", fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, "", fmt.Errorf("parse config: %w", err)
	}

	active, _ := cfg.Agent.Options["provider"].(string)
	return cfg.Agent.Providers, active, nil
}
