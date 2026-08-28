package config

import (
	"encoding/json"
	"testing"
)

func TestFillProviderDefaultsPreservesUserModelAndFillsBaseURL(t *testing.T) {
	base := defaults()
	cfg := Config{
		DefaultProvider: "deepseek",
		Providers: map[string]ProviderConfig{
			"deepseek": {Model: "deepseek-v4-pro", APIKey: "test-key"},
		},
	}
	fillProviderDefaults(&cfg, base)
	got := cfg.Provider("deepseek")
	if got.APIKey != "test-key" {
		t.Fatalf("api key changed: %q", got.APIKey)
	}
	if got.Model != "deepseek-v4-pro" {
		t.Fatalf("model not preserved: %q", got.Model)
	}
	if got.BaseURL != "https://api.deepseek.com/v1" {
		t.Fatalf("base url not filled: %q", got.BaseURL)
	}
	if got.MaxContext != 1000000 {
		t.Fatalf("max context = %d, want 1000000", got.MaxContext)
	}
}

func TestFillProviderDefaultsAfterJavaStyleConfigUnmarshal(t *testing.T) {
	base := defaults()
	cfg := defaults()
	raw := []byte(`{
	  "defaultProvider": "deepseek",
	  "providers": {
	    "deepseek": {
	      "apiKey": null,
	      "baseUrl": null,
	      "model": "deepseek-v4-pro"
	    }
	  }
	}`)
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	fillProviderDefaults(&cfg, base)
	got := cfg.Provider("deepseek")
	if got.Model != "deepseek-v4-pro" {
		t.Fatalf("model not preserved: %q", got.Model)
	}
	if got.BaseURL != "https://api.deepseek.com/v1" {
		t.Fatalf("base url not filled after unmarshal: %q", got.BaseURL)
	}
	if got.MaxContext != 1000000 {
		t.Fatalf("max context = %d, want 1000000", got.MaxContext)
	}
}

func TestApplyEnvDeepSeekV4UsesMillionContext(t *testing.T) {
	t.Setenv("DEEPSEEK_MODEL", "deepseek-v4-flash")
	base := defaults()
	cfg := defaults()
	applyEnv(&cfg)
	applyModelDefaults(&cfg, base)
	got := cfg.Provider("deepseek")
	if got.Model != "deepseek-v4-flash" {
		t.Fatalf("model = %q, want deepseek-v4-flash", got.Model)
	}
	if got.MaxContext != 1000000 {
		t.Fatalf("max context = %d, want 1000000", got.MaxContext)
	}
}
