package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"maunium.net/go/mautrix/event"
)

type Config struct {
	HomeserverURL  string `yaml:"homeserver_url"`
	ServerName     string `yaml:"server_name"`
	ASToken        string `yaml:"as_token"`
	HSToken        string `yaml:"hs_token"`
	ASAddress      string `yaml:"as_address"`
	AppserviceURL  string `yaml:"appservice_url"`
	WebhookAddress string `yaml:"webhook_address"`
	PublicBaseURL  string `yaml:"public_base_url"`
	DBPath         string `yaml:"db"`
	BotLocalpart   string `yaml:"bot_localpart"`
	BotDisplayName string `yaml:"bot_displayname"`
	UserPrefix     string `yaml:"user_prefix"`
	DefaultMsgtype string `yaml:"default_msgtype"`
}

func Default() *Config {
	return &Config{
		ASAddress:      "0.0.0.0:29329",
		DBPath:         "slackhooks.db",
		BotLocalpart:   "slackhooks",
		BotDisplayName: "Slack Hook",
		UserPrefix:     "_slackhook_",
		DefaultMsgtype: "m.text",
	}
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
	cfg.applyEnv()
	return cfg, nil
}

func (c *Config) applyEnv() {
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
		{"SLACKHOOKS_USER_PREFIX", &c.UserPrefix},
		{"SLACKHOOKS_DEFAULT_MSGTYPE", &c.DefaultMsgtype},
	}
	for _, o := range overrides {
		if val, ok := os.LookupEnv(o.key); ok && val != "" {
			*o.target = val
		}
	}
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
