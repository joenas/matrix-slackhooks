package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPath(t *testing.T) {
	t.Setenv("SLACKHOOKS_CONFIG", "")
	if got := DefaultPath(); got != "config.yaml" {
		t.Errorf("DefaultPath() with unset env = %q, want config.yaml", got)
	}
	t.Setenv("SLACKHOOKS_CONFIG", "/data/config.yaml")
	if got := DefaultPath(); got != "/data/config.yaml" {
		t.Errorf("DefaultPath() with env set = %q, want /data/config.yaml", got)
	}
}

func TestLoadEmptyPathUsesEnv(t *testing.T) {
	t.Setenv("SLACKHOOKS_HOMESERVER_URL", "https://matrix.example.com")
	t.Setenv("SLACKHOOKS_SERVER_NAME", "example.com")
	t.Setenv("SLACKHOOKS_BOT_DISPLAYNAME", "Hook Bot")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\") failed: %v", err)
	}
	if cfg.HomeserverURL != "https://matrix.example.com" || cfg.ServerName != "example.com" {
		t.Errorf("env overrides not applied: %+v", cfg)
	}
	if cfg.BotDisplayName != "Hook Bot" {
		t.Errorf("BotDisplayName = %q, want %q", cfg.BotDisplayName, "Hook Bot")
	}
}

func TestTokenFiles(t *testing.T) {
	dir := t.TempDir()
	asFile := filepath.Join(dir, "as_token")
	hsFile := filepath.Join(dir, "hs_token")
	if err := os.WriteFile(asFile, []byte("  as-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hsFile, []byte("hs-secret\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLACKHOOKS_AS_TOKEN", "env-wins-not")
	t.Setenv("SLACKHOOKS_HS_TOKEN", "env-wins-not")
	t.Setenv("SLACKHOOKS_AS_TOKEN_FILE", asFile)
	t.Setenv("SLACKHOOKS_HS_TOKEN_FILE", hsFile)
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.ASToken != "as-secret" {
		t.Errorf("ASToken = %q, want %q (file must win over env, whitespace trimmed)", cfg.ASToken, "as-secret")
	}
	if cfg.HSToken != "hs-secret" {
		t.Errorf("HSToken = %q, want %q", cfg.HSToken, "hs-secret")
	}
}

func TestTokenFilesEnvFallback(t *testing.T) {
	t.Setenv("SLACKHOOKS_AS_TOKEN", "env-as")
	t.Setenv("SLACKHOOKS_HS_TOKEN", "env-hs")
	t.Setenv("SLACKHOOKS_AS_TOKEN_FILE", "")
	t.Setenv("SLACKHOOKS_HS_TOKEN_FILE", "")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.ASToken != "env-as" || cfg.HSToken != "env-hs" {
		t.Errorf("plain env tokens not used: as=%q hs=%q", cfg.ASToken, cfg.HSToken)
	}
}

func TestTokenFileMissingIsError(t *testing.T) {
	t.Setenv("SLACKHOOKS_AS_TOKEN_FILE", filepath.Join(t.TempDir(), "nope"))
	if _, err := Load(""); err == nil {
		t.Fatal("expected an error for an unreadable *_FILE, got none")
	}
}
