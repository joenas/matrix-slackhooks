package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/joenas/matrix-slackhooks/internal/config"
)

func TestOpenConfigUsesSlackhooksConfigEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("homeserver_url: https://from-file\nserver_name: file.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLACKHOOKS_CONFIG", path)

	cfg, err := openConfig(config.DefaultPath())
	if err != nil {
		t.Fatalf("openConfig with $SLACKHOOKS_CONFIG file: %v", err)
	}
	if cfg.HomeserverURL != "https://from-file" || cfg.ServerName != "file.example" {
		t.Errorf("config from $SLACKHOOKS_CONFIG not loaded: %+v", cfg)
	}
}

func TestOpenConfigMissingDefaultFallsBackToEnv(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("SLACKHOOKS_CONFIG", missing)
	t.Setenv("SLACKHOOKS_HOMESERVER_URL", "https://from-env")
	t.Setenv("SLACKHOOKS_SERVER_NAME", "env.example")

	cfg, err := openConfig(config.DefaultPath())
	if err != nil {
		t.Fatalf("missing default config should fall back to defaults+env, got: %v", err)
	}
	if cfg.HomeserverURL != "https://from-env" || cfg.ServerName != "env.example" {
		t.Errorf("env-only fallback broken: %+v", cfg)
	}
	if cfg.DBPath != config.Default().DBPath {
		t.Errorf("defaults not applied: %+v", cfg)
	}

	// An explicit path that does not exist must still be an error.
	if _, err = openConfig(missing + ".other"); err == nil {
		t.Fatal("explicit missing config path should error")
	}
}

func TestResolveRoom(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{ServerName: "example.com", BotLocalpart: "slackhooks", HomeserverURL: "http://localhost:8008"}

	valid := []string{
		"!abc:example.com",
		"!opaque_id:example.com:8448",
	}
	for _, id := range valid {
		got, err := resolveRoom(ctx, cfg, id)
		if err != nil || got.String() != id {
			t.Errorf("resolveRoom(%q) = %q, %v; want %q", id, got, err, id)
		}
	}

	for _, bad := range []string{"", "abc:example.com", "!abc", "!:", "some random text"} {
		if got, err := resolveRoom(ctx, cfg, bad); err == nil {
			t.Errorf("resolveRoom(%q) = %q; expected an error", bad, got)
		}
	}

	// An alias needs a token to resolve; without one it must fail with a clear
	// message rather than silently accepting the alias.
	cfg.ASToken = ""
	if _, err := resolveRoom(ctx, cfg, "#room:example.com"); err == nil ||
		!strings.Contains(err.Error(), "as_token is not configured") {
		t.Errorf("alias without token: unexpected error %v", err)
	}
}

func TestUserNamespaceRegexes(t *testing.T) {
	userRaw, botRaw := userNamespaceRegexes("_slackhook_", "slackhooks", "localhost")
	userRe, err := regexp.Compile(userRaw)
	if err != nil {
		t.Fatalf("user namespace regex %q does not compile: %v", userRaw, err)
	}
	botRe, err := regexp.Compile(botRaw)
	if err != nil {
		t.Fatalf("bot namespace regex %q does not compile: %v", botRaw, err)
	}

	userMatches := []string{
		"@_slackhook_x:localhost",
		"@_slackhook_build-bot:localhost",
		"@_slackhook_:localhost",
	}
	for _, userID := range userMatches {
		if !userRe.MatchString(userID) {
			t.Errorf("user regex %q should match %s", userRaw, userID)
		}
	}
	userNonMatches := []string{
		"@slackhooks:localhost",
		"@_slackhook_x:example.com",
		"@someone:localhost",
		"@prefix_slackhook_x:localhost",
		"@_slackhook_x:localhost:1234",
	}
	for _, userID := range userNonMatches {
		if userRe.MatchString(userID) {
			t.Errorf("user regex %q should not match %s", userRaw, userID)
		}
	}

	if !botRe.MatchString("@slackhooks:localhost") {
		t.Errorf("bot regex %q should match the bot user", botRaw)
	}
	for _, userID := range []string{
		"@slackhooks:example.com",
		"@slackhooks_extra:localhost",
		"@_slackhook_x:localhost",
		"@slackhooks:localhost:1234",
	} {
		if botRe.MatchString(userID) {
			t.Errorf("bot regex %q should not match %s", botRaw, userID)
		}
	}
}

func TestUserNamespaceRegexesQuoted(t *testing.T) {
	userRaw, botRaw := userNamespaceRegexes("_slack.hook+", "bot.name", "ex ample.com")
	if !regexp.MustCompile(userRaw).MatchString("@_slack.hook+x:ex ample.com") {
		t.Errorf("regex %q should match a prefixed user with meta characters", userRaw)
	}
	if regexp.MustCompile(userRaw).MatchString("@_slackxhook+x:ex ample.com") {
		t.Errorf("regex %q should not match an unescaped variation", userRaw)
	}
	if !regexp.MustCompile(botRaw).MatchString("@bot.name:ex ample.com") {
		t.Errorf("bot regex %q should match the bot user with meta characters", botRaw)
	}
}
