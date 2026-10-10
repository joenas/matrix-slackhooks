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

	"github.com/rs/zerolog/log"
	"maunium.net/go/mautrix/id"
)

type DB struct {
	db *sql.DB
}

// migrations is the ordered list of schema migrations. Entry i upgrades the
// schema from PRAGMA user_version i to i+1, so len(migrations) is the schema
// version this binary expects. Append new entries at the end and never
// reorder or edit an existing one.
var migrations = []func(tx *sql.Tx) error{
	migrateV1,
	migrateV2,
}

// migrationV1SQL is the initial schema, matching the one-off schema the
// database was created with before migrations existed. It uses
// CREATE TABLE IF NOT EXISTS so it is a no-op on those existing databases and
// creates everything on a fresh one.
const migrationV1SQL = `
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

func migrateV1(tx *sql.Tx) error {
	_, err := tx.Exec(migrationV1SQL)
	return err
}

// migrationV2SQL adds the dms table used to remember per-user DM rooms for
// delivering webhook URLs.
const migrationV2SQL = `
CREATE TABLE IF NOT EXISTS dms (
    user_id TEXT PRIMARY KEY,
    room_id TEXT NOT NULL
);
`

func migrateV2(tx *sql.Tx) error {
	_, err := tx.Exec(migrationV2SQL)
	return err
}

// dsn builds the modernc.org/sqlite connection string. WAL mode and a busy
// timeout let a second process (the CLI, e.g. via docker exec) write to the
// database while the service holds it, without SQLITE_BUSY errors.
func dsn(path string) string {
	return "file:" + path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)"
}

func Open(path string) (*DB, error) {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}
	sqlDB, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}
	// modernc.org/sqlite is a single-writer database; one connection per
	// process avoids lock contention. Cross-process concurrency comes from WAL
	// plus the busy timeout in the DSN.
	sqlDB.SetMaxOpenConns(1)
	if err = migrate(sqlDB, path); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return &DB{db: sqlDB}, nil
}

// migrate brings the schema from its current PRAGMA user_version up to
// len(migrations), refusing to open a database written by a newer binary.
func migrate(sqlDB *sql.DB, path string) error {
	var version int
	if err := sqlDB.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	latest := len(migrations)
	if version > latest {
		return fmt.Errorf("database schema v%d is newer than this binary supports (v%d); refusing to downgrade", version, latest)
	} else if version == latest {
		return nil
	}
	// Back up any database that already holds a schema before migrating it. A
	// brand-new empty file (v0 with no tables) has nothing to back up.
	if version > 0 {
		if err := backupBeforeMigration(sqlDB, path, version); err != nil {
			return err
		}
	} else if hasSchema, err := hasTables(sqlDB); err != nil {
		return err
	} else if hasSchema {
		if err := backupBeforeMigration(sqlDB, path, version); err != nil {
			return err
		}
	}
	for i := version; i < latest; i++ {
		tx, err := sqlDB.Begin()
		if err != nil {
			return fmt.Errorf("begin migration v%d: %w", i+1, err)
		}
		if err = migrations[i](tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration v%d failed: %w", i+1, err)
		}
		// PRAGMA assignment does not accept a bound parameter; i+1 is a plain int.
		if _, err = tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("set schema version to v%d: %w", i+1, err)
		}
		if err = tx.Commit(); err != nil {
			return fmt.Errorf("commit migration v%d: %w", i+1, err)
		}
		log.Info().Int("version", i+1).Msg("Applied database schema migration")
	}
	return nil
}

func hasTables(sqlDB *sql.DB) (bool, error) {
	var n int
	if err := sqlDB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&n); err != nil {
		return false, fmt.Errorf("check for existing tables: %w", err)
	}
	return n > 0, nil
}

// backupBeforeMigration snapshots the database to a timestamped file next to
// the original and logs the path, so a failed migration can always be undone.
func backupBeforeMigration(sqlDB *sql.DB, path string, from int) error {
	dest := fmt.Sprintf("%s.bak-v%d-%s", path, from, time.Now().Format("20060102-150405"))
	if _, err := sqlDB.Exec(`VACUUM INTO ?`, dest); err != nil {
		return fmt.Errorf("pre-migration backup: %w", err)
	}
	log.Info().Str("path", dest).Int("from_version", from).Msg("Backed up database before schema migration")
	return nil
}

// Backup writes a consistent snapshot of the database to path using
// VACUUM INTO. It is safe to run against a live database (WAL mode); the
// destination must not already exist.
func (d *DB) Backup(path string) error {
	_, err := d.db.Exec(`VACUUM INTO ?`, path)
	return err
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

const hookColumns = `token, room_id, label, created_by, created_at`

type Hook struct {
	Token     string
	RoomID    id.RoomID
	Label     string
	CreatedBy string
	CreatedAt time.Time
}

type hookScanner interface {
	Scan(dest ...any) error
}

func scanHook(row hookScanner) (*Hook, error) {
	hook := &Hook{}
	var createdAt int64
	if err := row.Scan(&hook.Token, &hook.RoomID, &hook.Label, &hook.CreatedBy, &createdAt); err != nil {
		return nil, err
	}
	hook.CreatedAt = time.Unix(createdAt, 0)
	return hook, nil
}

func (d *DB) GetHook(token string) (*Hook, error) {
	row := d.db.QueryRow(`SELECT `+hookColumns+` FROM hooks WHERE token = ?`, token)
	hook, err := scanHook(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return hook, nil
}

// ListHooks returns hooks ordered by creation time. An empty roomID lists
// every hook, otherwise only hooks for that room.
func (d *DB) ListHooks(roomID id.RoomID) ([]Hook, error) {
	query := `SELECT ` + hookColumns + ` FROM hooks`
	var args []any
	if roomID != "" {
		query += ` WHERE room_id = ?`
		args = append(args, roomID.String())
	}
	query += ` ORDER BY created_at, token`
	return d.queryHooks(query, args...)
}

// FindHooksByPrefix returns hooks whose token starts with prefix. An empty
// prefix matches every hook; the caller decides whether a match set is usable.
func (d *DB) FindHooksByPrefix(prefix string) ([]Hook, error) {
	return d.queryHooks(
		`SELECT `+hookColumns+` FROM hooks WHERE substr(token, 1, ?) = ? ORDER BY token`,
		len(prefix), prefix)
}

func (d *DB) queryHooks(query string, args ...any) ([]Hook, error) {
	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hooks []Hook
	for rows.Next() {
		hook, err := scanHook(rows)
		if err != nil {
			return nil, err
		}
		hooks = append(hooks, *hook)
	}
	return hooks, rows.Err()
}

func (d *DB) InsertHook(hook *Hook) error {
	_, err := d.db.Exec(`INSERT INTO hooks (token, room_id, label, created_by, created_at) VALUES (?, ?, ?, ?, ?)`,
		hook.Token, hook.RoomID.String(), hook.Label, hook.CreatedBy, hook.CreatedAt.Unix())
	return err
}

// DeleteHook removes the hook with the given token. It is not an error if no
// such hook exists; callers resolving by prefix should check first.
func (d *DB) DeleteHook(token string) error {
	_, err := d.db.Exec(`DELETE FROM hooks WHERE token = ?`, token)
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

// GetDM returns the stored DM room ID for the user, or "" if none is stored.
func (d *DB) GetDM(userID id.UserID) (id.RoomID, error) {
	var roomID string
	err := d.db.QueryRow(`SELECT room_id FROM dms WHERE user_id = ?`, userID.String()).Scan(&roomID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	return id.RoomID(roomID), nil
}

// SetDM stores or updates the DM room ID for the user.
func (d *DB) SetDM(userID id.UserID, roomID id.RoomID) error {
	_, err := d.db.Exec(`
		INSERT INTO dms (user_id, room_id) VALUES (?, ?)
		ON CONFLICT (user_id) DO UPDATE SET room_id = excluded.room_id`,
		userID.String(), roomID.String())
	return err
}
