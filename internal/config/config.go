package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config 是完整配置结构体
type Config struct {
	Listen      ListenConfig          `yaml:"listen"`
	Security    SecurityConfig        `yaml:"security"`
	Upstreams   map[string]*Upstream  `yaml:"upstreams"`
	Tiers       map[string][]string   `yaml:"tiers"`
	Roles       map[string]*RoleEntry `yaml:"roles"`
	AutoUpgrade map[string]UpgradeRule `yaml:"auto_upgrade"`
	Logging     LoggingConfig         `yaml:"logging"`
	HealthCheck HealthCheckConfig     `yaml:"health_check"`
	Dashboard   DashboardConfig       `yaml:"dashboard"`
}

type ListenConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type SecurityConfig struct {
	APITokenEnv         string   `yaml:"api_token_env"`
	EnvFile             string   `yaml:"env_file"`
	PublicPaths         []string `yaml:"public_paths"`
	AllowLocalhostNoAuth bool    `yaml:"allow_localhost_no_auth"`
	MaxConsecutiveFails int      `yaml:"max_consecutive_fails"`
	CORSOrigins         []string `yaml:"cors_origins"`
	TrustCloudflare     bool     `yaml:"trust_cloudflare"`
	RateLimit           RateLimitConfig `yaml:"rate_limit"`
}

type RateLimitConfig struct {
	MaxPerMinute int `yaml:"max_per_minute"`
	MaxConcurrent int `yaml:"max_concurrent"`
}

type Upstream struct {
	Label       string            `yaml:"label"`
	Type        string            `yaml:"type"`
	BaseURL     string            `yaml:"base_url"`
	APIKey      string            `yaml:"api_key,omitempty"`
	APIKeyEnv   string            `yaml:"api_key_env,omitempty"`
	EnvFile     string            `yaml:"env_file,omitempty"`
	Timeout     int               `yaml:"timeout"`
	MaxRetries  int               `yaml:"max_retries"`
	RetryDelay  int               `yaml:"retry_delay"`
	Models      []string          `yaml:"models"`
	ExtraModels []string          `yaml:"extra_models,omitempty"`
}

func (u *Upstream) AllModels() []string {
	if len(u.ExtraModels) > 0 {
		result := make([]string, 0, len(u.Models)+len(u.ExtraModels))
		result = append(result, u.Models...)
		result = append(result, u.ExtraModels...)
		return result
	}
	return u.Models
}

type RoleEntry struct {
	Model    string             `yaml:"model"`
	Upstream string             `yaml:"upstream"`
	Fallback []FallbackEntry    `yaml:"fallback"`
}

type FallbackEntry struct {
	Model    string `yaml:"model"`
	Upstream string `yaml:"upstream"`
}

type UpgradeRule struct {
	TriggerHeader string `yaml:"trigger_header"`
	TriggerValue  string `yaml:"trigger_value"`
	TargetTier    string `yaml:"target_tier"`
}

type LoggingConfig struct {
	Dir        string `yaml:"dir"`
	UsageLog   string `yaml:"usage_log"`
	AccessLog  string `yaml:"access_log"`
	RotateDaily bool  `yaml:"rotate_daily"`
}

type HealthCheckConfig struct {
	Interval  int    `yaml:"interval"`
	Timeout   int    `yaml:"timeout"`
	TestPrompt string `yaml:"test_prompt"`
}

type DashboardConfig struct {
	Enabled        bool `yaml:"enabled"`
	RefreshInterval int  `yaml:"refresh_interval"`
}

// AppState 保存运行时全局状态
type AppState struct {
	Config     *Config
	StartTime  int64
	GatewayToken string
}

// 全局单例
var GlobalConfig *Config
var GlobalState *AppState

// Load 从文件加载配置
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// 默认值
	if cfg.Listen.Port == 0 {
		cfg.Listen.Port = 8650
	}
	if cfg.Listen.Host == "" {
		cfg.Listen.Host = "127.0.0.1"
	}
	if cfg.Security.MaxConsecutiveFails == 0 {
		cfg.Security.MaxConsecutiveFails = 3
	}
	if cfg.HealthCheck.Interval == 0 {
		cfg.HealthCheck.Interval = 300
	}
	if cfg.HealthCheck.Timeout == 0 {
		cfg.HealthCheck.Timeout = 10
	}
	if cfg.Logging.Dir == "" {
		cfg.Logging.Dir = "/app/logs"
	}
	if cfg.Logging.UsageLog == "" {
		cfg.Logging.UsageLog = "ai-model-gateway-usage.jsonl"
	}
	if cfg.Logging.AccessLog == "" {
		cfg.Logging.AccessLog = "ai-model-gateway.log"
	}
	if cfg.Security.RateLimit.MaxPerMinute == 0 {
		cfg.Security.RateLimit.MaxPerMinute = 60
	}
	if cfg.Security.RateLimit.MaxConcurrent == 0 {
		cfg.Security.RateLimit.MaxConcurrent = 16
	}

	GlobalConfig = &cfg
	return &cfg, nil
}

// ResolveAPIKey 从配置 + 环境变量 + env 文件中查找 API Key
func (u *Upstream) ResolveAPIKey() string {
	// 1. 直接配置
	if u.APIKey != "" {
		return u.APIKey
	}
	// 2. 环境变量
	if u.APIKeyEnv != "" {
		if v := os.Getenv(u.APIKeyEnv); v != "" {
			return v
		}
		// 3. .env 文件
		if u.EnvFile != "" {
			if v := readEnvFile(u.EnvFile, u.APIKeyEnv); v != "" {
				return v
			}
		}
	}
	return ""
}

func readEnvFile(path, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if strings.TrimSpace(parts[0]) == key {
			return strings.TrimSpace(parts[1])
		}
	}
	return ""
}

// LoadGatewayToken 加载网关认证 token
func LoadGatewayToken(cfg *Config) string {
	envVar := cfg.Security.APITokenEnv
	if envVar == "" {
		envVar = "GATEWAY_API_TOKEN"
	}
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	if cfg.Security.EnvFile != "" {
		if v := readEnvFile(cfg.Security.EnvFile, envVar); v != "" {
			return v
		}
	}
	return ""
}

// Addr 返回监听地址
func (l ListenConfig) Addr() string {
	return fmt.Sprintf("%s:%d", l.Host, l.Port)
}
