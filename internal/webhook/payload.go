package webhook

import (
	"encoding/json"
	"net/url"
	"strings"
)

type Attachment struct {
	Fallback string `json:"fallback"`
	Title    string `json:"title"`
	Text     string `json:"text"`
}

// Payload is the subset of the Slack incoming webhook payload format (plus
// some turt2live/matrix-appservice-webhooks fields) that we understand.
type Payload struct {
	Text        string `json:"text"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	IconURL     string `json:"icon_url"`
	AvatarURL   string `json:"avatar_url"`
	IconEmoji   string `json:"icon_emoji"`
	Format      string `json:"format"`

	Attachments []Attachment `json:"attachments"`
}

// SourceName returns the display name for the message source, using the
// Slack username field first and the turt2live displayName field as alias.
func (p *Payload) SourceName() string {
	if name := strings.TrimSpace(p.Username); name != "" {
		return name
	}
	if name := strings.TrimSpace(p.DisplayName); name != "" {
		return name
	}
	return ""
}

// SourceAvatarURL returns the avatar URL, using the Slack icon_url field
// first and the turt2live avatar_url field as alias.
func (p *Payload) SourceAvatarURL() string {
	if url := strings.TrimSpace(p.IconURL); url != "" {
		return url
	}
	if url := strings.TrimSpace(p.AvatarURL); url != "" {
		return url
	}
	return ""
}

func (p *Payload) Valid() bool {
	return strings.TrimSpace(p.Text) != "" || len(p.Attachments) > 0
}

// ParsePayload parses a webhook request body. It accepts JSON bodies,
// form-encoded bodies with the JSON in a `payload` field (as used by some
// Slack-compatible senders) and plain text bodies.
func ParsePayload(contentType string, body []byte) (*Payload, error) {
	base, _, _ := strings.Cut(contentType, ";")
	switch strings.TrimSpace(strings.ToLower(base)) {
	case "application/x-www-form-urlencoded":
		values, err := url.ParseQuery(strings.TrimSpace(string(body)))
		if err != nil {
			return nil, err
		}
		if payload := values.Get("payload"); payload != "" {
			p := &Payload{}
			if err = json.Unmarshal([]byte(payload), p); err != nil {
				return nil, err
			}
			return p, nil
		}
		return payloadFromValues(values), nil
	case "application/json", "text/json", "":
		trimmed := strings.TrimSpace(string(body))
		if trimmed != "" && !strings.HasPrefix(trimmed, "{") && base != "application/json" && base != "text/json" {
			return &Payload{Text: trimmed}, nil
		}
		p := &Payload{}
		if err := json.Unmarshal(body, p); err != nil {
			return nil, err
		}
		return p, nil
	default:
		p := &Payload{}
		err := json.Unmarshal(body, p)
		if err != nil {
			return &Payload{Text: string(body)}, nil
		}
		return p, nil
	}
}

func payloadFromValues(values url.Values) *Payload {
	return &Payload{
		Text:        values.Get("text"),
		Username:    values.Get("username"),
		DisplayName: values.Get("displayName"),
		IconURL:     values.Get("icon_url"),
		AvatarURL:   values.Get("avatar_url"),
		IconEmoji:   values.Get("icon_emoji"),
		Format:      values.Get("format"),
	}
}
