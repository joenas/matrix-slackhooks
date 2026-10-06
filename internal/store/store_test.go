package store

import (
	"testing"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestHooks(t *testing.T) {
	db := openTestDB(t)
	hook := &Hook{Token: "abc123", RoomID: "!room:example.com", Label: "CI", CreatedBy: "me", CreatedAt: time.Now().Truncate(time.Second)}
	if err := db.InsertHook(hook); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := db.GetHook("abc123")
	if err != nil || got == nil {
		t.Fatalf("get: %v, %v", got, err)
	}
	if got.RoomID != hook.RoomID || got.Label != "CI" || got.CreatedBy != "me" {
		t.Errorf("unexpected hook: %+v", got)
	}
	if got, err = db.GetHook("nope"); got != nil || err != nil {
		t.Errorf("unknown token should return nil, got %v, %v", got, err)
	}
	if has, err := db.HasHooksForRoom("!room:example.com"); err != nil || !has {
		t.Errorf("HasHooksForRoom: %v, %v", has, err)
	}
	if has, err := db.HasHooksForRoom("!other:example.com"); err != nil || has {
		t.Errorf("HasHooksForRoom other: %v, %v", has, err)
	}
}

func TestPuppets(t *testing.T) {
	db := openTestDB(t)
	if got, err := db.GetPuppet("nobody"); got != nil || err != nil {
		t.Errorf("expected nil puppet: %v, %v", got, err)
	}
	p := &Puppet{Localpart: "hook_bot", DisplayName: "Hook Bot", AvatarSourceURL: "https://example.com/a.png", AvatarMXC: "mxc://example.com/abc"}
	if err := db.UpsertPuppet(p); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := db.GetPuppet("hook_bot")
	if err != nil || got == nil || got.DisplayName != "Hook Bot" || got.AvatarMXC != "mxc://example.com/abc" {
		t.Fatalf("unexpected puppet: %+v, %v", got, err)
	}
	got.DisplayName = "Renamed"
	if err := db.UpsertPuppet(got); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	again, _ := db.GetPuppet("hook_bot")
	if again.DisplayName != "Renamed" {
		t.Errorf("expected update, got %q", again.DisplayName)
	}
}

func TestAvatars(t *testing.T) {
	db := openTestDB(t)
	if _, ok := db.GetAvatar("https://example.com/none.png"); ok {
		t.Error("expected cache miss")
	}
	if err := db.PutAvatar("https://example.com/a.png", "mxc://example.com/abc"); err != nil {
		t.Fatalf("put: %v", err)
	}
	mxc, ok := db.GetAvatar("https://example.com/a.png")
	if !ok || mxc != "mxc://example.com/abc" {
		t.Errorf("unexpected cache hit: %q, %v", mxc, ok)
	}
}

func TestStateStoreMemberships(t *testing.T) {
	db := openTestDB(t)
	ss := NewStateStore(db)
	room := id.RoomID("!room:example.com")
	user := id.UserID("_slackhook_bot:example.com")
	ctx := t.Context()

	if ss.IsInRoom(ctx, room, user) {
		t.Error("should not be in room yet")
	}
	if err := ss.SetMembership(ctx, room, user, event.MembershipJoin); err != nil {
		t.Fatalf("set membership: %v", err)
	}
	if !ss.IsInRoom(ctx, room, user) || !ss.IsInvited(ctx, room, user) {
		t.Error("should be in room")
	}
	if !ss.IsMembership(ctx, room, user, event.MembershipJoin) {
		t.Error("IsMembership join should be true")
	}

	member := &event.MemberEventContent{Membership: event.MembershipLeave}
	if err := ss.SetMember(ctx, room, user, member); err != nil {
		t.Fatalf("set member: %v", err)
	}
	if ss.IsInRoom(ctx, room, user) {
		t.Error("leave should clear room membership")
	}

	if err := ss.ForgetMembership(ctx, room, user); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if ss.IsMembership(ctx, room, user, event.MembershipLeave) {
		t.Error("membership should be forgotten")
	}
}
