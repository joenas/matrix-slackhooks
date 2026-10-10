package bridge

import (
	"net/netip"
	"testing"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/joenas/matrix-slackhooks/internal/config"
	"github.com/joenas/matrix-slackhooks/internal/store"
)

func TestRoomAllowed(t *testing.T) {
	room := id.RoomID("!abc:example.com")
	other := id.RoomID("!zzz:example.com")

	// A room with a hook is always allowed, even when not listed.
	if !roomAllowed(room, true, nil, nil) {
		t.Error("room with a hook must be allowed")
	}

	// Explicit room-ID allowlist entry.
	allowed := []string{"!abc:example.com", "#ci:example.com"}
	if !roomAllowed(room, false, allowed, nil) {
		t.Error("room listed by ID must be allowed")
	}
	if roomAllowed(other, false, allowed, nil) {
		t.Error("unlisted room must not be allowed")
	}

	// Alias allowlist entries resolve through the supplied resolver.
	resolve := func(alias id.RoomAlias) (id.RoomID, bool) {
		if alias == "#ci:example.com" {
			return room, true
		}
		return "", false
	}
	if !roomAllowed(room, false, []string{"#ci:example.com"}, resolve) {
		t.Error("alias resolving to the room must allow it")
	}
	if roomAllowed(other, false, []string{"#ci:example.com"}, resolve) {
		t.Error("alias must not allow a different room")
	}
	// An alias that cannot be resolved must not allow the room, and a nil
	// resolver must simply skip alias entries instead of crashing.
	if roomAllowed(room, false, []string{"#ci:example.com"}, nil) {
		t.Error("nil resolver must not allow alias entries")
	}
	unresolvable := func(id.RoomAlias) (id.RoomID, bool) { return "", false }
	if roomAllowed(room, false, []string{"#ci:example.com"}, unresolvable) {
		t.Error("unresolvable alias must not allow the room")
	}

	// Whitespace and empty entries are ignored.
	if roomAllowed(room, false, []string{"", "   "}, nil) {
		t.Error("empty allowlist entries must not allow anything")
	}
}

func TestBotInviteAllowed(t *testing.T) {
	admin := id.UserID("@alice:example.com")
	nonAdmin := id.UserID("@bob:example.com")
	isAdmin := func(uid id.UserID) bool { return uid == admin }

	// Room allowed → invite accepted regardless of inviter.
	if !botInviteAllowed(true, nonAdmin, isAdmin) {
		t.Error("allowed room must accept invite from non-admin")
	}
	if !botInviteAllowed(true, admin, isAdmin) {
		t.Error("allowed room must accept invite from admin")
	}

	// Room not allowed, inviter is admin → accepted.
	if !botInviteAllowed(false, admin, isAdmin) {
		t.Error("admin inviter must be accepted even for unlisted room")
	}

	// Room not allowed, inviter is not admin → rejected.
	if botInviteAllowed(false, nonAdmin, isAdmin) {
		t.Error("non-admin inviter must be rejected for unlisted room")
	}
}

// ---------- Command parsing ----------

func TestParseCommand(t *testing.T) {
	const prefix = "!hook"
	tests := []struct {
		name string
		body string
		sub  string
		args string
		ok   bool
	}{
		{"exact prefix only", "!hook", "", "", true},
		{"prefix with space, no subcommand", "!hook ", "", "", true},
		{"new no label", "!hook new", "new", "", true},
		{"new with label", "!hook new grafana", "new", "grafana", true},
		{"new with label containing spaces", "!hook new my cool label", "new", "my cool label", true},
		{"list", "!hook list", "list", "", true},
		{"remove with query", "!hook remove grafana", "remove", "grafana", true},
		{"remove with prefix", "!hook remove abc12345", "remove", "abc12345", true},
		{"help", "!hook help", "help", "", true},
		{"unknown subcommand", "!hook frobnicate", "frobnicate", "", true},
		{"case-insensitive subcommand", "!hook NEW stuff", "new", "stuff", true},
		{"leading whitespace", "  !hook list", "list", "", true},
		{"trailing whitespace", "!hook list  ", "list", "", true},
		{"not a command", "hello world", "", "", false},
		{"wrong prefix", "!webhook list", "", "", false},
		{"prefix is substring", "!!hook list", "", "", false},
		{"empty body", "", "", "", false},
		{"just text", "hello", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := parseCommand(tc.body, prefix)
			if !tc.ok {
				if cmd != nil {
					t.Fatalf("parseCommand(%q) = %+v, want nil", tc.body, cmd)
				}
				return
			}
			if cmd == nil {
				t.Fatalf("parseCommand(%q) = nil, want sub=%q args=%q", tc.body, tc.sub, tc.args)
			}
			if cmd.Subcommand != tc.sub {
				t.Errorf("Subcommand = %q, want %q", cmd.Subcommand, tc.sub)
			}
			if cmd.Args != tc.args {
				t.Errorf("Args = %q, want %q", cmd.Args, tc.args)
			}
		})
	}
}

// ---------- Permission check ----------

func TestCanRunCommand(t *testing.T) {
	// Admin bypasses power level.
	if !canRunCommand(true, 0, 50) {
		t.Error("admin with power 0 should be allowed")
	}
	// Non-admin at threshold.
	if !canRunCommand(false, 50, 50) {
		t.Error("power level at threshold should be allowed")
	}
	// Non-admin above threshold.
	if !canRunCommand(false, 99, 50) {
		t.Error("power level above threshold should be allowed")
	}
	// Non-admin below threshold.
	if canRunCommand(false, 49, 50) {
		t.Error("power level below threshold should be denied")
	}
	// 101 = admins only: even power 100 is denied for non-admins.
	if canRunCommand(false, 100, 101) {
		t.Error("power 100 with threshold 101 should be denied for non-admins")
	}
	if !canRunCommand(true, 100, 101) {
		t.Error("admin should bypass threshold 101")
	}
}

// ---------- Remove matching ----------

func TestMatchHooksForRemoval(t *testing.T) {
	room := id.RoomID("!room:example.com")
	other := id.RoomID("!other:example.com")
	hooks := []store.Hook{
		{Token: "aaaaaa1111111111", RoomID: room, Label: "grafana"},
		{Token: "bbbbbb2222222222", RoomID: room, Label: "CI"},
		{Token: "cccccc3333333333", RoomID: room, Label: "prometheus"},
		{Token: "ddddd444444444444", RoomID: other, Label: "grafana"},
	}

	// Exact label match (case-insensitive), one match.
	m := matchHooksForRemoval(hooks, room, "grafana")
	if len(m) != 1 || m[0].Token != "aaaaaa1111111111" {
		t.Errorf("label 'grafana' should match only the first hook, got %d: %+v", len(m), m)
	}

	// Case-insensitive label match for "PROMETHEUS" (different case).
	m = matchHooksForRemoval(hooks, room, "PROMETHEUS")
	if len(m) != 1 || m[0].Token != "cccccc3333333333" {
		t.Errorf("label 'PROMETHEUS' should match the third hook, got %d: %+v", len(m), m)
	}

	// Token prefix match (>= 4 chars).
	m = matchHooksForRemoval(hooks, room, "aaaa")
	if len(m) != 1 || m[0].Token != "aaaaaa1111111111" {
		t.Errorf("prefix 'aaaa' should match one hook, got %d: %+v", len(m), m)
	}

	// Token prefix too short (< 4 chars) - should not match by prefix.
	m = matchHooksForRemoval(hooks, room, "aaa")
	if len(m) != 0 {
		t.Errorf("prefix 'aaa' is too short, should not match, got %d", len(m))
	}

	// Ambiguous: multiple hooks with same label in same room.
	dupHooks := []store.Hook{
		{Token: "xxxx1111111111111", RoomID: room, Label: "dup"},
		{Token: "yyyy2222222222222", RoomID: room, Label: "dup"},
	}
	m = matchHooksForRemoval(dupHooks, room, "dup")
	if len(m) != 2 {
		t.Errorf("ambiguous label should match 2, got %d", len(m))
	}

	// Ambiguous via case-insensitivity: "DUP" matches both.
	m = matchHooksForRemoval(dupHooks, room, "DUP")
	if len(m) != 2 {
		t.Errorf("case-insensitive ambiguous label should match 2, got %d", len(m))
	}

	// Other room's hook not matched.
	m = matchHooksForRemoval(hooks, room, "ddddd")
	if len(m) != 0 {
		t.Errorf("hook in other room must not match, got %d", len(m))
	}

	// No match at all.
	m = matchHooksForRemoval(hooks, room, "nonexistent")
	if len(m) != 0 {
		t.Errorf("nonexistent query should match nothing, got %d", len(m))
	}

	// Empty query matches nothing.
	m = matchHooksForRemoval(hooks, room, "")
	if len(m) != 0 {
		t.Errorf("empty query should match nothing, got %d", len(m))
	}

	// Full token match also works (prefix of full length).
	m = matchHooksForRemoval(hooks, room, "bbbbbb2222222222")
	if len(m) != 1 || m[0].Token != "bbbbbb2222222222" {
		t.Errorf("full token should match one hook, got %d: %+v", len(m), m)
	}
}

// ---------- Command candidate filter ----------

func TestCommandCandidate(t *testing.T) {
	botID := id.UserID("@slackhooks-bot:example.com")
	puppet := func(uid id.UserID) bool {
		return uid == id.UserID("@_slackhook_builder:example.com")
	}
	dmRoom := id.RoomID("!dm:example.com")
	isDMRoom := func(rid id.RoomID) bool { return rid == dmRoom }
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	validBase := &event.Event{
		Sender:    id.UserID("@alice:example.com"),
		RoomID:    id.RoomID("!room:example.com"),
		Timestamp: now.UnixMilli(),
		Content:   event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgText, Body: "!hook list"}},
	}

	tests := []struct {
		name  string
		mod   func(*event.Event)
		allow bool
	}{
		{
			name:  "valid command event passes",
			mod:   func(_ *event.Event) {},
			allow: true,
		},
		{
			name: "bot's own message is ignored",
			mod: func(e *event.Event) {
				e.Sender = botID
			},
			allow: false,
		},
		{
			name: "puppet message is ignored",
			mod: func(e *event.Event) {
				e.Sender = id.UserID("@_slackhook_builder:example.com")
			},
			allow: false,
		},
		{
			name: "non-MessageEventContent is ignored",
			mod: func(e *event.Event) {
				e.Content = event.Content{Parsed: nil}
			},
			allow: false,
		},
		{
			name: "non-m.text message type is ignored",
			mod: func(e *event.Event) {
				e.Content = event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgNotice, Body: "!hook list"}}
			},
			allow: false,
		},
		{
			name: "edit (m.replace) is ignored",
			mod: func(e *event.Event) {
				e.Content = event.Content{Parsed: &event.MessageEventContent{
					MsgType:   event.MsgText,
					Body:      "!hook list",
					RelatesTo: &event.RelatesTo{Type: event.RelReplace},
				}}
			},
			allow: false,
		},
		{
			name: "event older than maxCommandAge is ignored",
			mod: func(e *event.Event) {
				e.Timestamp = now.Add(-maxCommandAge - time.Minute).UnixMilli()
			},
			allow: false,
		},
		{
			name: "event just within maxCommandAge passes",
			mod: func(e *event.Event) {
				e.Timestamp = now.Add(-maxCommandAge + time.Second).UnixMilli()
			},
			allow: true,
		},
		{
			name: "DM room is ignored",
			mod: func(e *event.Event) {
				e.RoomID = id.RoomID("!dm:example.com")
			},
			allow: false,
		},
		{
			name: "non-DM room passes",
			mod: func(e *event.Event) {
				e.RoomID = id.RoomID("!other:example.com")
			},
			allow: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			evt := *validBase
			tc.mod(&evt)
			got := commandCandidate(&evt, botID, puppet, isDMRoom, now)
			if got != tc.allow {
				t.Errorf("commandCandidate = %v, want %v", got, tc.allow)
			}
		})
	}
}

// ---------- NormalizeLabel ----------

func TestNormalizeLabel(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"empty label", "", "", false},
		{"simple label", "grafana", "grafana", false},
		{"exactly 32 characters", "abcdefghijklmnopqrstuvwxyz012345", "abcdefghijklmnopqrstuvwxyz012345", false},
		{"33 characters", "abcdefghijklmnopqrstuvwxyz0123456", "", true},
		{"multibyte within limit", "日本語日本語日本語日本語", "日本語日本語日本語日本語", false},
		{"multibyte exactly 32 runes", "日本語日本語日本語日本語日本語日本語日本語日本語日本語日本語日本", "日本語日本語日本語日本語日本語日本語日本語日本語日本語日本語日本", false},
		{"multibyte 33 runes", "日本語日本語日本語日本語日本語日本語日本語日本語日本語日本語日本語", "", true},
		{"surrounding whitespace trimmed", "  grafana  ", "grafana", false},
		{"newline rejected", "line1\nline2", "", true},
		{"tab rejected", "label\twith\ttab", "", true},
		{"carriage return rejected", "label\rreturn", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.NormalizeLabel(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("NormalizeLabel(%q) = %q, want error", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Errorf("NormalizeLabel(%q) unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("NormalizeLabel(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// ---------- Config.IsAdmin via bridge ----------

func TestIsAdminWithConfig(t *testing.T) {
	cfg := &config.Config{
		Admins: []string{
			"@alice:example.com",
			"@*:admin.example.com",
		},
	}
	if !cfg.IsAdmin("@alice:example.com") {
		t.Error("exact admin must match")
	}
	if !cfg.IsAdmin("@anyone:admin.example.com") {
		t.Error("@*:server pattern must match any localpart on that server")
	}
	if cfg.IsAdmin("@alice:other.example.com") {
		t.Error("user on wrong server must not match @*:admin.example.com")
	}
	if cfg.IsAdmin("@bob:example.com") {
		t.Error("non-listed user must not be admin")
	}
	if cfg.IsAdmin("@mallory:admin.example.com.evil.com") {
		t.Error("server suffix attack must not match")
	}
}

// ---------- Token short prefix ----------

func TestTokenShortPrefix(t *testing.T) {
	if got := tokenShortPrefix("abcdef0123456789"); got != "abcdef01" {
		t.Errorf("tokenShortPrefix = %q, want %q", got, "abcdef01")
	}
	if got := tokenShortPrefix("abc"); got != "abc" {
		t.Errorf("short token = %q, want %q", got, "abc")
	}
	if got := tokenShortPrefix(""); got != "" {
		t.Errorf("empty token = %q, want %q", got, "")
	}
}

// ---------- Avatar guard ----------

func TestIsLocalIP(t *testing.T) {
	mustAddr := func(s string) netip.Addr {
		a, err := netip.ParseAddr(s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return a
	}

	// Standard ranges still blocked.
	for _, ip := range []string{"127.0.0.1", "::1", "10.0.0.1", "192.168.1.1", "172.16.0.1", "169.254.1.1"} {
		if !isLocalIP(mustAddr(ip)) {
			t.Errorf("isLocalIP(%s) = false, want true", ip)
		}
	}

	// New ranges.
	for _, ip := range []string{
		"0.0.0.1",         // 0.0.0.0/8
		"0.1.2.3",         // 0.0.0.0/8
		"100.64.0.1",      // 100.64.0.0/10 (CGNAT)
		"100.127.255.254", // 100.64.0.0/10
		"192.0.0.1",       // 192.0.0.0/24
		"198.18.0.1",      // 198.18.0.0/15
		"198.19.255.254",  // 198.18.0.0/15
	} {
		if !isLocalIP(mustAddr(ip)) {
			t.Errorf("isLocalIP(%s) = false, want true", ip)
		}
	}

	// NAT64 (64:ff9b::/96).
	for _, ip := range []string{
		"64:ff9b::1",
		"64:ff9b::1.2.3.4",
	} {
		if !isLocalIP(mustAddr(ip)) {
			t.Errorf("isLocalIP(%s) = false, want true", ip)
		}
	}

	// IPv4-mapped IPv6 forms of new ranges.
	for _, ip := range []string{
		"::ffff:0.1.2.3",    // 0.0.0.0/8 mapped
		"::ffff:100.64.0.1", // CGNAT mapped
		"::ffff:192.0.0.1",  // 192.0.0.0/24 mapped
		"::ffff:198.18.0.1", // 198.18.0.0/15 mapped
	} {
		if !isLocalIP(mustAddr(ip)) {
			t.Errorf("isLocalIP(%s) = false, want true (IPv4-mapped)", ip)
		}
	}

	// Public addresses must not be blocked.
	for _, ip := range []string{
		"1.1.1.1",
		"8.8.8.8",
		"2606:4700:4700::1111",
		"2001:4860:4860::8888",
	} {
		if isLocalIP(mustAddr(ip)) {
			t.Errorf("isLocalIP(%s) = true, want false (public)", ip)
		}
	}

	// 100.63.x and 100.128.x are outside CGNAT range.
	if isLocalIP(mustAddr("100.63.0.1")) {
		t.Error("100.63.0.1 should not be blocked (just below CGNAT)")
	}
	if isLocalIP(mustAddr("100.128.0.1")) {
		t.Error("100.128.0.1 should not be blocked (just above CGNAT)")
	}

	// 198.17.x and 198.20.x are outside 198.18.0.0/15.
	if isLocalIP(mustAddr("198.17.0.1")) {
		t.Error("198.17.0.1 should not be blocked (just below 198.18.0.0/15)")
	}
	if isLocalIP(mustAddr("198.20.0.1")) {
		t.Error("198.20.0.1 should not be blocked (just above 198.18.0.0/15)")
	}
}
