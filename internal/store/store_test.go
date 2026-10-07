package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestMain(m *testing.M) {
	log.Logger = zerolog.Nop()
	os.Exit(m.Run())
}

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func userVersion(t *testing.T, db *DB) int {
	t.Helper()
	var v int
	if err := db.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	return v
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

func TestMembershipKeyedByFullUserID(t *testing.T) {
	db := openTestDB(t)
	ss := NewStateStore(db)
	room := id.RoomID("!room:example.com")
	onion := id.UserID("_slackhook_bot:example.com")
	elsewhere := id.UserID("_slackhook_bot:other.example")
	ctx := t.Context()

	if err := ss.SetMembership(ctx, room, onion, event.MembershipJoin); err != nil {
		t.Fatalf("set membership: %v", err)
	}
	if !ss.IsInRoom(ctx, room, onion) {
		t.Error("user should be in room")
	}
	if ss.IsInRoom(ctx, room, elsewhere) {
		t.Error("same localpart on another server must have its own membership entry")
	}
	if err := ss.SetMembership(ctx, room, elsewhere, event.MembershipInvite); err != nil {
		t.Fatalf("set membership for other server: %v", err)
	}
	if !ss.IsInvited(ctx, room, elsewhere) || ss.IsInRoom(ctx, room, elsewhere) {
		t.Error("other-server membership should be independent")
	}
	if !ss.IsInRoom(ctx, room, onion) {
		t.Error("first user membership should be unaffected")
	}

	if err := ss.ForgetMembership(ctx, room, elsewhere); err != nil {
		t.Fatalf("forget other server: %v", err)
	}
	if !ss.IsInRoom(ctx, room, onion) {
		t.Error("forgetting the other server's membership must not affect this one")
	}
	if ss.IsMembership(ctx, room, elsewhere, event.MembershipJoin, event.MembershipInvite) {
		t.Error("other-server membership should be forgotten")
	}
}

func TestMigrationsFresh(t *testing.T) {
	path := t.TempDir() + "/fresh.db"
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if got := userVersion(t, db); got != len(migrations) {
		t.Errorf("user_version = %d, want %d", got, len(migrations))
	}
	if baks, _ := filepath.Glob(path + ".bak-*"); len(baks) != 0 {
		t.Errorf("fresh db must not create backups, got %v", baks)
	}
	// The schema must be present after migration.
	if _, err := db.GetHook("anything"); err != nil {
		t.Errorf("hooks table missing after migration: %v", err)
	}
}

func TestMigrationsLegacy(t *testing.T) {
	path := t.TempDir() + "/legacy.db"
	// Create a database with the exact pre-migration schema, user_version 0,
	// and a hook row, exactly as the first released binary left it.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(migrationV1SQL); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if _, err = raw.Exec(`INSERT INTO hooks (token, room_id, label, created_by, created_at) VALUES (?, ?, ?, ?, ?)`,
		"legacytoken", "!legacy:example.com", "old", "me", int64(1000)); err != nil {
		t.Fatalf("insert legacy hook: %v", err)
	}
	var uv int
	if err = raw.QueryRow(`PRAGMA user_version`).Scan(&uv); err != nil {
		t.Fatal(err)
	} else if uv != 0 {
		t.Fatalf("expected legacy db user_version 0, got %d", uv)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	defer db.Close()

	if got := userVersion(t, db); got != len(migrations) {
		t.Errorf("user_version = %d after migrate, want %d", got, len(migrations))
	}
	hook, err := db.GetHook("legacytoken")
	if err != nil || hook == nil {
		t.Fatalf("legacy hook lost during migration: %v, %v", hook, err)
	}
	if hook.RoomID != "!legacy:example.com" || hook.Label != "old" || hook.CreatedBy != "me" {
		t.Errorf("legacy hook changed during migration: %+v", hook)
	}
	if baks, _ := filepath.Glob(path + ".bak-v0-*"); len(baks) != 1 {
		t.Errorf("expected exactly one pre-migration backup, got %v", baks)
	}
}

func TestMigrationsTooNew(t *testing.T) {
	path := t.TempDir() + "/future.db"
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", len(migrations)+1)); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err = Open(path); err == nil {
		t.Fatal("expected Open to refuse a schema newer than this binary")
	} else if !strings.Contains(err.Error(), "newer than this binary supports") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestConcurrentHandlesWrite(t *testing.T) {
	path := t.TempDir() + "/concurrent.db"
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(path)
	if err != nil {
		t.Fatalf("second handle: %v", err)
	}
	defer b.Close()

	// Hold an uncommitted write transaction on A. While it is held, B must
	// block (not fail with SQLITE_BUSY) thanks to WAL + busy_timeout, then
	// succeed once A commits.
	tx, err := a.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO hooks (token, room_id, created_at) VALUES ('a1', '!a:x', 1)`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	committed := make(chan error, 1)
	go func() {
		time.Sleep(150 * time.Millisecond)
		committed <- tx.Commit()
	}()

	if _, err = b.db.Exec(`INSERT INTO hooks (token, room_id, created_at) VALUES ('b1', '!b:x', 2)`); err != nil {
		t.Fatalf("second handle write failed (busy_timeout/WAL not working): %v", err)
	}
	if err = <-committed; err != nil {
		t.Fatalf("commit A: %v", err)
	}
	if h, err := a.GetHook("a1"); err != nil || h == nil {
		t.Errorf("row from handle A missing: %v, %v", h, err)
	}
	if h, err := b.GetHook("b1"); err != nil || h == nil {
		t.Errorf("row from handle B missing: %v, %v", h, err)
	}
}

func TestListAndDeleteHooks(t *testing.T) {
	db := openTestDB(t)
	base := time.Now().Truncate(time.Second)
	mk := func(token, room, label string, age time.Duration) *Hook {
		return &Hook{Token: token, RoomID: id.RoomID(room), Label: label, CreatedBy: "by", CreatedAt: base.Add(-age)}
	}
	for _, h := range []*Hook{
		mk("aaa1", "!one:example.com", "one", 3*time.Hour),
		mk("bbb2", "!two:example.com", "two", 2*time.Hour),
		mk("aaa3", "!one:example.com", "three", 1*time.Hour),
	} {
		if err := db.InsertHook(h); err != nil {
			t.Fatal(err)
		}
	}

	all, err := db.ListHooks("")
	if err != nil {
		t.Fatal(err)
	} else if len(all) != 3 {
		t.Fatalf("ListHooks all: want 3, got %d", len(all))
	}
	// Ordered oldest -> newest by created_at.
	if all[0].Token != "aaa1" || all[1].Token != "bbb2" || all[2].Token != "aaa3" {
		t.Errorf("ListHooks not ordered by created_at: %+v", all)
	}

	one, err := db.ListHooks("!one:example.com")
	if err != nil {
		t.Fatal(err)
	} else if len(one) != 2 {
		t.Errorf("ListHooks room one: want 2, got %d", len(one))
	}

	if got, err := db.FindHooksByPrefix("aaa"); err != nil || len(got) != 2 {
		t.Errorf("FindHooksByPrefix many: got %d, %v", len(got), err)
	}
	if got, err := db.FindHooksByPrefix("bbb2"); err != nil || len(got) != 1 || got[0].Token != "bbb2" {
		t.Errorf("FindHooksByPrefix unique: got %+v, %v", got, err)
	}
	if got, err := db.FindHooksByPrefix("zzz"); err != nil || len(got) != 0 {
		t.Errorf("FindHooksByPrefix none: got %d, %v", len(got), err)
	}
	if got, err := db.FindHooksByPrefix(""); err != nil || len(got) != 3 {
		t.Errorf("FindHooksByPrefix empty: got %d, %v", len(got), err)
	}

	if err = db.DeleteHook("aaa1"); err != nil {
		t.Fatal(err)
	}
	if h, err := db.GetHook("aaa1"); err != nil || h != nil {
		t.Errorf("aaa1 should be deleted: %v, %v", h, err)
	}
	if after, _ := db.ListHooks("!one:example.com"); len(after) != 1 {
		t.Errorf("room one after delete: want 1, got %d", len(after))
	}
	if err = db.DeleteHook("nonexistent"); err != nil {
		t.Errorf("deleting a missing token should not error: %v", err)
	}
}

func TestBackup(t *testing.T) {
	db := openTestDB(t)
	if err := db.InsertHook(&Hook{Token: "bktok", RoomID: "!bk:example.com", Label: "bk", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir() + "/snapshot.db"
	if err := db.Backup(dest); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("backup file missing: %v", err)
	}

	snap, err := Open(dest)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer snap.Close()
	if h, err := snap.GetHook("bktok"); err != nil || h == nil || h.Label != "bk" {
		t.Errorf("snapshot is missing the hook: %v, %v", h, err)
	}

	if err = db.Backup(dest); err == nil {
		t.Error("backing up over an existing file should error")
	}
}
