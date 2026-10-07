package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"maunium.net/go/mautrix/id"
)

type DB struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS hooks (
	token TEXT PRIMARY KEY,
	room_id TEXT NOT NULL,
	label TEXT NOT NULL DEFAULT '',
	created_by TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS puppets (
	localpart TEXT PRIMARY KEY,
	display_name TEXT NOT NULL DEFAULT '',
	avatar_source_url TEXT NOT NULL DEFAULT '',
	avatar_mxc TEXT NOT NULL DEFAULT '',
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS memberships (
	user_id TEXT NOT NULL,
	room_id TEXT NOT NULL,
	membership TEXT NOT NULL,
	PRIMARY KEY (user_id, room_id)
);
CREATE TABLE IF NOT EXISTS avatars (
	source_url TEXT PRIMARY KEY,
	mxc TEXT NOT NULL,
	fetched_at INTEGER NOT NULL
);
`

func Open(path string) (*DB, error) {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// modernc.org/sqlite is a single-writer database; one connection avoids lock contention.
	sqlDB.SetMaxOpenConns(1)
	if _, err = sqlDB.Exec(schema); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &DB{db: sqlDB}, nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

func NewToken() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Errorf("generate webhook token: %w", err))
	}
	return hex.EncodeToString(buf)
}

type Hook struct {
	Token     string
	RoomID    id.RoomID
	Label     string
	CreatedBy string
	CreatedAt time.Time
}

func (d *DB) GetHook(token string) (*Hook, error) {
	row := d.db.QueryRow(`SELECT token, room_id, label, created_by, created_at FROM hooks WHERE token = ?`, token)
	hook := &Hook{}
	var createdAt int64
	err := row.Scan(&hook.Token, &hook.RoomID, &hook.Label, &hook.CreatedBy, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	hook.CreatedAt = time.Unix(createdAt, 0)
	return hook, nil
}

func (d *DB) InsertHook(hook *Hook) error {
	_, err := d.db.Exec(`INSERT INTO hooks (token, room_id, label, created_by, created_at) VALUES (?, ?, ?, ?, ?)`,
		hook.Token, hook.RoomID.String(), hook.Label, hook.CreatedBy, hook.CreatedAt.Unix())
	return err
}

func (d *DB) HasHooksForRoom(roomID id.RoomID) (bool, error) {
	var exists int
	err := d.db.QueryRow(`SELECT 1 FROM hooks WHERE room_id = ? LIMIT 1`, roomID.String()).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

type Puppet struct {
	Localpart       string
	DisplayName     string
	AvatarSourceURL string
	AvatarMXC       string
	UpdatedAt       time.Time
}

func (d *DB) GetPuppet(localpart string) (*Puppet, error) {
	row := d.db.QueryRow(`SELECT localpart, display_name, avatar_source_url, avatar_mxc, updated_at FROM puppets WHERE localpart = ?`, localpart)
	puppet := &Puppet{}
	var updatedAt int64
	err := row.Scan(&puppet.Localpart, &puppet.DisplayName, &puppet.AvatarSourceURL, &puppet.AvatarMXC, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	puppet.UpdatedAt = time.Unix(updatedAt, 0)
	return puppet, nil
}

func (d *DB) UpsertPuppet(puppet *Puppet) error {
	now := time.Now().Unix()
	_, err := d.db.Exec(`
		INSERT INTO puppets (localpart, display_name, avatar_source_url, avatar_mxc, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (localpart) DO UPDATE SET
			display_name = excluded.display_name,
			avatar_source_url = excluded.avatar_source_url,
			avatar_mxc = excluded.avatar_mxc,
			updated_at = excluded.updated_at`,
		puppet.Localpart, puppet.DisplayName, puppet.AvatarSourceURL, puppet.AvatarMXC, now)
	return err
}

func (d *DB) GetAvatar(sourceURL string) (string, bool) {
	var mxc string
	err := d.db.QueryRow(`SELECT mxc FROM avatars WHERE source_url = ?`, sourceURL).Scan(&mxc)
	if err != nil {
		return "", false
	}
	return mxc, true
}

func (d *DB) PutAvatar(sourceURL, mxc string) error {
	_, err := d.db.Exec(`
		INSERT INTO avatars (source_url, mxc, fetched_at) VALUES (?, ?, ?)
		ON CONFLICT (source_url) DO UPDATE SET mxc = excluded.mxc, fetched_at = excluded.fetched_at`,
		sourceURL, mxc, time.Now().Unix())
	return err
}

// GetMembership returns the cached membership of a full user ID in a room,
// or "" when there is no cache entry.
func (d *DB) GetMembership(userID id.UserID, roomID id.RoomID) (string, error) {
	var membership string
	err := d.db.QueryRow(`SELECT membership FROM memberships WHERE user_id = ? AND room_id = ?`, userID.String(), roomID.String()).Scan(&membership)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return membership, err
}

func (d *DB) SetMembership(userID id.UserID, roomID id.RoomID, membership string) error {
	_, err := d.db.Exec(`
		INSERT INTO memberships (user_id, room_id, membership) VALUES (?, ?, ?)
		ON CONFLICT (user_id, room_id) DO UPDATE SET membership = excluded.membership`,
		userID.String(), roomID.String(), membership)
	return err
}

func (d *DB) DeleteMembership(userID id.UserID, roomID id.RoomID) error {
	_, err := d.db.Exec(`DELETE FROM memberships WHERE user_id = ? AND room_id = ?`, userID.String(), roomID.String())
	return err
}
