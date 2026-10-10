package config

import (
	"os"
	"path/filepath"
	"testing"

	"maunium.net/go/mautrix/id"
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

func TestDefaultsForNewFields(t *testing.T) {
	cfg := Default()
	if cfg.CommandPowerLevel != 50 {
		t.Errorf("CommandPowerLevel = %d, want 50", cfg.CommandPowerLevel)
	}
	if cfg.CommandPrefix != "!hook" {
		t.Errorf("CommandPrefix = %q, want %q", cfg.CommandPrefix, "!hook")
	}
}

func TestIsAdmin(t *testing.T) {
	cfg := &Config{
		Admins: []string{
			"@alice:example.com",
			"@*:admin.example.com",
			"  @bob:example.com  ", // whitespace should be trimmed
		},
	}
	if !cfg.IsAdmin("@alice:example.com") {
		t.Error("exact match should be admin")
	}
	if !cfg.IsAdmin("@bob:example.com") {
		t.Error("whitespace-padded entry should match")
	}
	if !cfg.IsAdmin("@anybody:admin.example.com") {
		t.Error("@*:server pattern should match any localpart on that server")
	}
	if cfg.IsAdmin("@alice:other.example.com") {
		t.Error("user on wrong server should not match exact entry")
	}
	if cfg.IsAdmin("@alice:admin.example.com.evil.com") {
		t.Error("server suffix attack should not match @*:admin.example.com")
	}
	if cfg.IsAdmin("@mallory:example.com") {
		t.Error("unlisted user should not be admin")
	}
}

func TestIsAdminEmpty(t *testing.T) {
	cfg := &Config{}
	if cfg.IsAdmin("@anyone:example.com") {
		t.Error("no admins configured should deny everyone")
	}
}

func TestEnvAdminsAndPowerLevel(t *testing.T) {
	t.Setenv("SLACKHOOKS_ADMINS", "@alice:example.com, @bob:example.com")
	t.Setenv("SLACKHOOKS_COMMAND_POWER_LEVEL", "101")
	t.Setenv("SLACKHOOKS_COMMAND_PREFIX", "!webhook")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Admins) != 2 {
		t.Fatalf("Admins = %v, want 2 entries", cfg.Admins)
	}
	if cfg.Admins[0] != "@alice:example.com" {
		t.Errorf("Admins[0] = %q", cfg.Admins[0])
	}
	if cfg.Admins[1] != "@bob:example.com" {
		t.Errorf("Admins[1] = %q", cfg.Admins[1])
	}
	if cfg.CommandPowerLevel != 101 {
		t.Errorf("CommandPowerLevel = %d, want 101", cfg.CommandPowerLevel)
	}
	if cfg.CommandPrefix != "!webhook" {
		t.Errorf("CommandPrefix = %q, want !webhook", cfg.CommandPrefix)
	}
	if !cfg.IsAdmin(id.UserID("@alice:example.com")) {
		t.Error("env admin should be recognized")
	}
}

func TestLoadAdminsFromYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := []byte(`
homeserver_url: https://matrix.example.com
server_name: example.com
admins:
  - "@alice:example.com"
  - "@*:admin.example.com"
command_power_level: 75
command_prefix: "!wh"
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.IsAdmin(id.UserID("@alice:example.com")) {
		t.Error("YAML admin should be recognized")
	}
	if !cfg.IsAdmin(id.UserID("@anyone:admin.example.com")) {
		t.Error("YAML pattern should be recognized")
	}
	if cfg.CommandPowerLevel != 75 {
		t.Errorf("CommandPowerLevel = %d, want 75", cfg.CommandPowerLevel)
	}
	if cfg.CommandPrefix != "!wh" {
		t.Errorf("CommandPrefix = %q, want !wh", cfg.CommandPrefix)
	}
}
