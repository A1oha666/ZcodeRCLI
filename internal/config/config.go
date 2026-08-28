package config

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	DefaultProvider string                    `json:"default_provider"`
	Providers       map[string]ProviderConfig `json:"providers"`
	Web             WebConfig                 `json:"web"`
}

type ProviderConfig struct {
	APIKey          string `json:"api_key"`
	BaseURL         string `json:"base_url"`
	Model           string `json:"model"`
	MaxContext      int    `json:"max_context"`
	SupportsImages  bool   `json:"supports_images"`
	SupportsCaching bool   `json:"supports_caching"`
}

type WebConfig struct {
	SerpAPIKey string `json:"serpapi_key"`
	SearxngURL string `json:"searxng_url"`
}

func Load() Config {
	loadDotEnv(".env")
	loadDotEnv(filepath.Join(HomeDir(), ".env"))

	base := defaults()
	cfg := defaults()
	path := filepath.Join(HomeDir(), ".zcoder", "config.json")
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}
	fillProviderDefaults(&cfg, base)
	applyEnv(&cfg)
	applyModelDefaults(&cfg, base)
	if cfg.DefaultProvider == "" {
		cfg.DefaultProvider = firstConfiguredProvider(cfg)
	}
	if cfg.DefaultProvider == "" {
		cfg.DefaultProvider = "deepseek"
	}
	return cfg
}

func fillProviderDefaults(cfg *Config, base Config) {
	if cfg.Providers == nil {
		cfg.Providers = map[string]ProviderConfig{}
	}
	for name, def := range base.Providers {
		current := cfg.Providers[name]
		if current.BaseURL == "" {
			current.BaseURL = def.BaseURL
		}
		if current.Model == "" {
			current.Model = def.Model
		}
		if current.MaxContext == 0 {
			current.MaxContext = modelDefaultMaxContext(name, current.Model, def.MaxContext)
		}
		if !current.SupportsImages {
			current.SupportsImages = def.SupportsImages
		}
		if !current.SupportsCaching {
			current.SupportsCaching = def.SupportsCaching
		}
		cfg.Providers[name] = current
	}
	if cfg.DefaultProvider == "" {
		cfg.DefaultProvider = base.DefaultProvider
	}
	applyModelDefaults(cfg, base)
}

func applyModelDefaults(cfg *Config, base Config) {
	for name, provider := range cfg.Providers {
		def := base.Providers[name]
		derived := modelDefaultMaxContext(name, provider.Model, def.MaxContext)
		if provider.MaxContext == 0 || (def.MaxContext > 0 && provider.MaxContext == def.MaxContext && derived > provider.MaxContext) {
			provider.MaxContext = derived
		}
		cfg.Providers[name] = provider
	}
}

func modelDefaultMaxContext(provider, model string, fallback int) int {
	if strings.EqualFold(provider, "deepseek") && strings.Contains(strings.ToLower(model), "v4") {
		return 1000000
	}
	return fallback
}

func defaults() Config {
	return Config{
		DefaultProvider: "deepseek",
		Providers: map[string]ProviderConfig{
			"deepseek": {
				BaseURL:    "https://api.deepseek.com/v1",
				Model:      "deepseek-chat",
				MaxContext: 128000,
			},
			"glm": {
				BaseURL:        "https://open.bigmodel.cn/api/paas/v4",
				Model:          "glm-5.1",
				MaxContext:     200000,
				SupportsImages: true,
			},
			"step": {
				BaseURL:    "https://api.stepfun.com/v1",
				Model:      "step-3.5-flash",
				MaxContext: 256000,
			},
			"kimi": {
				BaseURL:    "https://api.moonshot.cn/v1",
				Model:      "kimi-k2",
				MaxContext: 256000,
			},
			"freellmapi": {
				BaseURL:    "http://localhost:5173/v1",
				Model:      "auto",
				MaxContext: 128000,
			},
			"agnes": {
				BaseURL:    "https://apihub.agnes-ai.com/v1",
				Model:      "agnes-2.0-flash",
				MaxContext: 1000000,
			},
			"xfyun": {
				BaseURL:    "https://maas-api.cn-huabei-1.xf-yun.com/v1",
				Model:      "x1",
				MaxContext: 128000,
			},
			"openai": {
				BaseURL:        "https://api.openai.com/v1",
				Model:          "gpt-4.1",
				MaxContext:     128000,
				SupportsImages: true,
			},
		},
	}
}

func (c Config) Provider(name string) ProviderConfig {
	if p, ok := c.Providers[strings.ToLower(name)]; ok {
		return p
	}
	return ProviderConfig{}
}

func (c Config) Save() error {
	dir := filepath.Join(HomeDir(), ".zcoder")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.json"), b, 0o600)
}

func HomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "."
	}
	return home
}

func applyEnv(cfg *Config) {
	envs := map[string]string{
		"deepseek":   "DEEPSEEK_API_KEY",
		"glm":        "GLM_API_KEY",
		"step":       "STEP_API_KEY",
		"kimi":       "KIMI_API_KEY",
		"freellmapi": "FREELLMAPI_API_KEY",
		"agnes":      "AGNES_API_KEY",
		"xfyun":      "XFYUN_MAAS_API_KEY",
		"openai":     "OPENAI_API_KEY",
	}
	for name, keyEnv := range envs {
		p := cfg.Providers[name]
		if v := os.Getenv(keyEnv); v != "" {
			p.APIKey = v
		}
		if v := os.Getenv(strings.ToUpper(name) + "_BASE_URL"); v != "" {
			p.BaseURL = v
		}
		if v := os.Getenv(strings.ToUpper(name) + "_MODEL"); v != "" {
			p.Model = v
		}
		cfg.Providers[name] = p
	}
	if v := os.Getenv("ZCODER_PROVIDER"); v != "" {
		cfg.DefaultProvider = strings.ToLower(v)
	}
	if v := os.Getenv("SERPAPI_API_KEY"); v != "" {
		cfg.Web.SerpAPIKey = v
	}
	if v := os.Getenv("SEARXNG_BASE_URL"); v != "" {
		cfg.Web.SearxngURL = strings.TrimRight(v, "/")
	}
}

func firstConfiguredProvider(cfg Config) string {
	for _, name := range []string{"deepseek", "glm", "step", "kimi", "agnes", "freellmapi", "openai", "xfyun"} {
		if cfg.Provider(name).APIKey != "" {
			return name
		}
	}
	return ""
}

func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		key := strings.TrimSpace(parts[0])
		value := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		if key != "" && os.Getenv(key) == "" {
			_ = os.Setenv(key, value)
		}
	}
}
