package bridge

import (
	"testing"

	"maunium.net/go/mautrix/id"
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
