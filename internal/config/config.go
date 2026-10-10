package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type Config struct {
	HomeserverURL     string   `yaml:"homeserver_url"`
	ServerName        string   `yaml:"server_name"`
	ASToken           string   `yaml:"as_token"`
	HSToken           string   `yaml:"hs_token"`
	ASAddress         string   `yaml:"as_address"`
	AppserviceURL     string   `yaml:"appservice_url"`
	WebhookAddress    string   `yaml:"webhook_address"`
	PublicBaseURL     string   `yaml:"public_base_url"`
	DBPath            string   `yaml:"db"`
	BotLocalpart      string   `yaml:"bot_localpart"`
	BotDisplayName    string   `yaml:"bot_displayname"`
	UserPrefix        string   `yaml:"user_prefix"`
	DefaultMsgtype    string   `yaml:"default_msgtype"`
	AllowedRooms      []string `yaml:"allowed_rooms"`
	Admins            []string `yaml:"admins"`
	CommandPowerLevel int      `yaml:"command_power_level"`
	CommandPrefix     string   `yaml:"command_prefix"`
}

func Default() *Config {
	return &Config{
		ASAddress:         "0.0.0.0:29329",
		DBPath:            "slackhooks.db",
		BotLocalpart:      "slackhooks",
		BotDisplayName:    "Slack Hook",
		UserPrefix:        "_slackhook_",
		DefaultMsgtype:    "m.text",
		CommandPowerLevel: 50,
		CommandPrefix:     "!hook",
	}
}

// DefaultPath returns the config file path used when -config is not given:
// $SLACKHOOKS_CONFIG if set, otherwise "config.yaml".
func DefaultPath() string {
	if p := os.Getenv("SLACKHOOKS_CONFIG"); p != "" {
		return p
	}
	return "config.yaml"
}

func Load(path string) (*Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if err = yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config file %s: %w", path, err)
		}
	}
	if err := cfg.applyEnv(); err != nil {
		return nil, err
	}
	if err := cfg.applyEnvTokenFiles(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) applyEnv() error {
	overrides := []struct {
		key    string
		target *string
	}{
		{"SLACKHOOKS_HOMESERVER_URL", &c.HomeserverURL},
		{"SLACKHOOKS_SERVER_NAME", &c.ServerName},
		{"SLACKHOOKS_AS_TOKEN", &c.ASToken},
		{"SLACKHOOKS_HS_TOKEN", &c.HSToken},
		{"SLACKHOOKS_AS_ADDRESS", &c.ASAddress},
		{"SLACKHOOKS_APPSERVICE_URL", &c.AppserviceURL},
		{"SLACKHOOKS_WEBHOOK_ADDRESS", &c.WebhookAddress},
		{"SLACKHOOKS_PUBLIC_BASE_URL", &c.PublicBaseURL},
		{"SLACKHOOKS_DB", &c.DBPath},
		{"SLACKHOOKS_BOT_LOCALPART", &c.BotLocalpart},
		{"SLACKHOOKS_BOT_DISPLAYNAME", &c.BotDisplayName},
		{"SLACKHOOKS_USER_PREFIX", &c.UserPrefix},
		{"SLACKHOOKS_DEFAULT_MSGTYPE", &c.DefaultMsgtype},
		{"SLACKHOOKS_COMMAND_PREFIX", &c.CommandPrefix},
	}
	for _, o := range overrides {
		if val, ok := os.LookupEnv(o.key); ok && val != "" {
			*o.target = val
		}
	}
	// CommandPowerLevel is an int, so it gets a dedicated override.
	if val, ok := os.LookupEnv("SLACKHOOKS_COMMAND_POWER_LEVEL"); ok && val != "" {
		n, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("SLACKHOOKS_COMMAND_POWER_LEVEL: invalid integer %q: %w", val, err)
		}
		c.CommandPowerLevel = n
	}
	// allowed_rooms is a list, so it gets a dedicated comma-separated override.
	if val, ok := os.LookupEnv("SLACKHOOKS_ALLOWED_ROOMS"); ok {
		var rooms []string
		for _, room := range strings.Split(val, ",") {
			if room = strings.TrimSpace(room); room != "" {
				rooms = append(rooms, room)
			}
		}
		c.AllowedRooms = rooms
	}
	// admins is a list, so it gets a dedicated comma-separated override.
	if val, ok := os.LookupEnv("SLACKHOOKS_ADMINS"); ok {
		var admins []string
		for _, a := range strings.Split(val, ",") {
			if a = strings.TrimSpace(a); a != "" {
				admins = append(admins, a)
			}
		}
		c.Admins = admins
	}
	return nil
}

// applyEnvTokenFiles loads secrets from files (Docker/Swarm secrets). The
// *_FILE variables take precedence over the plain env var overrides.
func (c *Config) applyEnvTokenFiles() error {
	overrides := []struct {
		key    string
		target *string
	}{
		{"SLACKHOOKS_AS_TOKEN_FILE", &c.ASToken},
		{"SLACKHOOKS_HS_TOKEN_FILE", &c.HSToken},
	}
	for _, o := range overrides {
		path, ok := os.LookupEnv(o.key)
		if !ok || path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", o.key, err)
		}
		*o.target = strings.TrimSpace(string(data))
	}
	return nil
}

func (c *Config) Validate() error {
	if c.HomeserverURL == "" {
		return fmt.Errorf("homeserver_url is required")
	} else if c.ServerName == "" {
		return fmt.Errorf("server_name is required")
	} else if c.BotLocalpart == "" {
		return fmt.Errorf("bot_localpart is required")
	}
	return nil
}

func (c *Config) ValidateTokens() error {
	if c.ASToken == "" || c.HSToken == "" {
		return fmt.Errorf("as_token and hs_token are required, run `slackhooks generate-registration` and copy the tokens into the config")
	}
	return nil
}

func (c *Config) MsgType() event.MessageType {
	switch strings.ToLower(strings.TrimPrefix(c.DefaultMsgtype, "m.")) {
	case "notice":
		return event.MsgNotice
	default:
		return event.MsgText
	}
}

// RegistrationURL returns the URL the homeserver should use to reach the
// appservice, falling back to the listen address if no explicit URL is set.
func (c *Config) RegistrationURL() string {
	if c.AppserviceURL != "" {
		return c.AppserviceURL
	}
	addr := c.ASAddress
	if strings.HasPrefix(addr, "0.0.0.0:") || strings.HasPrefix(addr, "[::]:") {
		idx := strings.LastIndex(addr, ":")
		addr = "localhost" + addr[idx:]
	}
	return "http://" + addr
}

// WebhookBaseURL returns the public base URL used when printing webhook URLs.
func (c *Config) WebhookBaseURL() string {
	base := strings.TrimSuffix(c.PublicBaseURL, "/")
	if base == "" {
		base = "http://" + c.ASAddress
	}
	return base
}

// IsAdmin reports whether userID is an admin. An admin entry is an exact user
// ID or a pattern "@*:server" where * matches the whole localpart. This is a
// pure function with no Matrix or database calls.
func (c *Config) IsAdmin(userID id.UserID) bool {
	for _, entry := range c.Admins {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if entry == userID.String() {
			return true
		}
		if strings.HasPrefix(entry, "@*:") {
			server := entry[3:]
			_, userServer, err := userID.Parse()
			if err == nil && userServer == server {
				return true
			}
		}
	}
	return false
}
