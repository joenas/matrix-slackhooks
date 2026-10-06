package webhook

import (
	"net/url"
	"testing"
)

func TestParsePayloadJSON(t *testing.T) {
	p, err := ParsePayload("application/json", []byte(`{
		"text": "Hello *world*",
		"username": "Build Bot",
		"icon_url": "https://example.com/icon.png",
		"icon_emoji": ":ghost:",
		"attachments": [{"fallback": "the fallback", "text": "attachment text"}]
	}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Text != "Hello *world*" {
		t.Errorf("unexpected text: %q", p.Text)
	}
	if p.SourceName() != "Build Bot" {
		t.Errorf("unexpected source name: %q", p.SourceName())
	}
	if p.SourceAvatarURL() != "https://example.com/icon.png" {
		t.Errorf("unexpected avatar url: %q", p.SourceAvatarURL())
	}
	if p.IconEmoji != ":ghost:" {
		t.Errorf("unexpected icon_emoji: %q", p.IconEmoji)
	}
	if len(p.Attachments) != 1 || p.Attachments[0].Fallback != "the fallback" {
		t.Errorf("unexpected attachments: %+v", p.Attachments)
	}
	if !p.Valid() {
		t.Error("payload should be valid")
	}
}

func TestParsePayloadTurt2liveFields(t *testing.T) {
	p, err := ParsePayload("application/json", []byte(`{
		"text": "<b>hi</b>",
		"displayName": "Legacy Bot",
		"avatar_url": "https://example.com/avatar.png",
		"format": "html"
	}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.SourceName() != "Legacy Bot" {
		t.Errorf("unexpected source name: %q", p.SourceName())
	}
	if p.SourceAvatarURL() != "https://example.com/avatar.png" {
		t.Errorf("unexpected avatar url: %q", p.SourceAvatarURL())
	}
	if p.Format != "html" {
		t.Errorf("unexpected format: %q", p.Format)
	}
}

func TestParsePayloadSlackFieldsWinOverAliases(t *testing.T) {
	p, err := ParsePayload("", []byte(`{
		"text": "hi",
		"username": "slack name",
		"displayName": "legacy name",
		"icon_url": "https://example.com/a.png",
		"avatar_url": "https://example.com/b.png"
	}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.SourceName() != "slack name" {
		t.Errorf("unexpected source name: %q", p.SourceName())
	}
	if p.SourceAvatarURL() != "https://example.com/a.png" {
		t.Errorf("unexpected avatar url: %q", p.SourceAvatarURL())
	}
}

func TestParsePayloadFormEncodedPayloadField(t *testing.T) {
	form := url.Values{}
	form.Set("payload", `{"text": "form hello", "username": "Form Bot"}`)
	p, err := ParsePayload("application/x-www-form-urlencoded", []byte(form.Encode()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Text != "form hello" || p.SourceName() != "Form Bot" {
		t.Errorf("unexpected payload: %+v", p)
	}
}

func TestParsePayloadFormEncodedFlat(t *testing.T) {
	form := url.Values{}
	form.Set("text", "flat hello")
	form.Set("username", "Flat Bot")
	form.Set("icon_emoji", ":tada:")
	p, err := ParsePayload("application/x-www-form-urlencoded; charset=utf-8", []byte(form.Encode()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Text != "flat hello" || p.SourceName() != "Flat Bot" || p.IconEmoji != ":tada:" {
		t.Errorf("unexpected payload: %+v", p)
	}
}

func TestParsePayloadPlainBody(t *testing.T) {
	p, err := ParsePayload("text/plain", []byte("just some text"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Text != "just some text" {
		t.Errorf("unexpected text: %q", p.Text)
	}
}

func TestParsePayloadInvalid(t *testing.T) {
	if _, err := ParsePayload("application/json", []byte(`{invalid`)); err == nil {
		t.Error("expected an error for invalid JSON")
	}
	form := url.Values{"payload": []string{`{invalid`}}
	if _, err := ParsePayload("application/x-www-form-urlencoded", []byte(form.Encode())); err == nil {
		t.Error("expected an error for invalid payload JSON")
	}
}

func TestPayloadValidity(t *testing.T) {
	if (&Payload{}).Valid() {
		t.Error("empty payload should not be valid")
	}
	if (&Payload{Text: "   "}).Valid() {
		t.Error("whitespace-only payload should not be valid")
	}
	if !(&Payload{Attachments: []Attachment{{Text: "x"}}}).Valid() {
		t.Error("attachment-only payload should be valid")
	}
}

func TestLookupEmoji(t *testing.T) {
	if got := LookupEmoji(":ghost:"); got != "👻" {
		t.Errorf("unexpected emoji: %q", got)
	}
	if got := LookupEmoji("+1"); got != "👍" {
		t.Errorf("unexpected emoji: %q", got)
	}
	if got := LookupEmoji(":totally_unknown:"); got != ":totally_unknown:" {
		t.Errorf("unknown shortcode should be kept raw, got %q", got)
	}
}
