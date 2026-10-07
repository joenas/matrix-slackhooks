package webhook

import (
	"regexp"
	"strings"
	"testing"
)

func TestLocalpart(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"build-bot", "_slackhook_build-bot"},
		{"a.b-c=d_1x", "_slackhook_a.b-c=d_1x"},
		// Anything that is not already a valid lowercase localpart gets a
		// hash, so names differing only in case/separators never collide.
		{"My Bot", ""},
		{"J.R. Dobell=Quux_x-2", ""},
		{"  spaces  everywhere  ", ""},
		{"Ünïcödé Bot", ""}, // checked with a prefix match below
		{"日本語", ""},
		{"", "_slackhook_webhook"},
		{"!!!", ""},
		{strings.Repeat("a", 100), ""},
	}
	for _, test := range tests {
		got := Localpart("_slackhook_", test.name)
		switch test.want {
		case "":
			if !strings.HasPrefix(got, "_slackhook_") {
				t.Errorf("Localpart(%q): missing prefix: %q", test.name, got)
			}
			if !regexp.MustCompile(`[0-9a-f]{8}$`).MatchString(got) {
				t.Errorf("Localpart(%q): expected a trailing short hash: %q", test.name, got)
			}
		default:
			if got != test.want {
				t.Errorf("Localpart(%q): got %q, want %q", test.name, got, test.want)
			}
		}
	}
}

func TestLocalpartAllowedChars(t *testing.T) {
	for _, name := range []string{"My Bot", "Ünïcödé Bot", "日本語", "", "!!!", strings.Repeat("x", 200)} {
		got := Localpart("_slackhook_", name)
		rest := strings.TrimPrefix(got, "_slackhook_")
		if rest == "" {
			t.Errorf("Localpart(%q): empty localpart", name)
		}
		for _, r := range rest {
			ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || strings.ContainsRune("._=-", r)
			if !ok {
				t.Errorf("Localpart(%q): disallowed character %q in %q", name, r, got)
			}
		}
		if len(rest) > maxSlugLength+1+hashLength {
			t.Errorf("Localpart(%q): too long: %q", name, got)
		}
	}
}

func TestLocalpartDifferentNamesDontCollide(t *testing.T) {
	a := Localpart("_slackhook_", "日本語ボット")
	b := Localpart("_slackhook_", "另一个机器人")
	if a == b {
		t.Errorf("non-ASCII names collided: %q", a)
	}
}

func TestLocalpartLossyASCIINameCollisions(t *testing.T) {
	names := []string{"Build Bot", "build bot", "Build-Bot", "Build  Bot", "build.bot"}
	seen := map[string]string{}
	for _, name := range names {
		got := Localpart("_slackhook_", name)
		if prev, ok := seen[got]; ok {
			t.Errorf("names %q and %q collided on %q", prev, name, got)
		}
		seen[got] = name
	}
	// A name that already is a valid lowercase localpart stays hash-free.
	if got := Localpart("_slackhook_", "build-bot"); got != "_slackhook_build-bot" {
		t.Errorf("already-valid name changed: %q", got)
	}
}

func TestLocalpartLongTruncatedWithHash(t *testing.T) {
	name := strings.Repeat("ab ", 60)
	got := Localpart("_slackhook_", name)
	if !strings.HasPrefix(got, "_slackhook_ab-ab") {
		t.Errorf("unexpected prefix for long name: %q", got)
	}
	if !regexp.MustCompile(`[0-9a-f]{8}$`).MatchString(got) {
		t.Errorf("expected a trailing short hash: %q", got)
	}
}
