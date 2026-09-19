// Package storage owns per-OS-user, non-secret application configuration.
// It must not be used for credentials, capabilities, chat, or event history.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	_ "modernc.org/sqlite"
)

var (
	ErrStorage      = errors.New("設定データを利用できません。保存先の権限と空き容量を確認してください")
	ErrInvalid      = errors.New("設定データの形式または値が不正です")
	ErrFutureSchema = errors.New("新しいバージョンの設定データです。対応するアプリで開いてください")
	ErrNotFound     = errors.New("設定データが見つかりません")
)

// Store is safe for concurrent callers. Close it after its users have stopped.
// The SQL connection is deliberately private: callers use typed repositories.
type Store struct{ db *sql.DB }

// Open opens the current OS user's configuration, never the working directory.
func Open(ctx context.Context) (*Store, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return nil, ErrStorage
	}
	if err := os.MkdirAll(base, 0700); err != nil {
		return nil, ErrStorage
	}
	// Resolve OS-provided aliases (e.g. /var on macOS) before securing our subtree.
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		return nil, ErrStorage
	}
	return openAt(ctx, filepath.Join(base, "tsumikit"))
}

// openAt takes an application-owned directory under an existing trusted parent.
// Only tests choose a path; Wails and imported packages cannot select it.
func openAt(ctx context.Context, dir string) (*Store, error) {
	if !filepath.IsAbs(dir) {
		return nil, ErrStorage
	}
	if err := secureDirectory(dir); err != nil {
		return nil, ErrStorage
	}
	path := filepath.Join(dir, "settings.db")
	for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
		p := path + suffix
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, ErrStorage
		}
		if err := secureFile(p); err != nil {
			return nil, ErrStorage
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		err = f.Close()
	} else if errors.Is(err, os.ErrExist) {
		err = nil
	}
	if err != nil {
		return nil, ErrStorage
	}
	if err = secureFile(path); err != nil {
		return nil, ErrStorage
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	if filepath.VolumeName(path) != "" {
		u.Path = "/" + u.Path
	}
	q := url.Values{"mode": {"rw"}, "_pragma": {"foreign_keys(1)", "busy_timeout(2000)", "synchronous(FULL)", "temp_store(MEMORY)"}, "_txlock": {"immediate"}}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, ErrStorage
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db}
	if err = s.migrate(ctx, migrations); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return storageError(s.db.Close()) }

func storageError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	// Driver errors can include values or local paths. Never expose them to UI/logs.
	return ErrStorage
}

// A single transaction covers all pending migrations and their version marker.
// Unknown/newer versions are rejected without overwriting or resetting the DB.
func (s *Store) migrate(ctx context.Context, steps []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError(err)
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return storageError(err)
	}
	if version < 0 || version > len(steps) {
		return ErrFutureSchema
	}
	for i := version; i < len(steps); i++ {
		if _, err = tx.ExecContext(ctx, steps[i]); err != nil {
			return storageError(err)
		}
		if _, err = tx.ExecContext(ctx, "PRAGMA user_version = "+strconv.Itoa(i+1)); err != nil {
			return storageError(err)
		}
	}
	return storageError(tx.Commit())
}

var migrations = []string{`
CREATE TABLE app_settings (
 id INTEGER PRIMARY KEY CHECK (id = 1),
 overlay_port INTEGER NOT NULL CHECK (overlay_port BETWEEN 1024 AND 65535)
) STRICT;
INSERT INTO app_settings VALUES (1, 18500);
CREATE TABLE stream_inputs (
 platform TEXT NOT NULL CHECK (platform IN ('youtube','twitch')),
 account_id TEXT NOT NULL CHECK (length(account_id) BETWEEN 1 AND 256),
 stream_url TEXT NOT NULL CHECK (length(stream_url) BETWEEN 1 AND 2048),
 PRIMARY KEY (platform, account_id)
) STRICT;
CREATE TABLE packages (
 id TEXT PRIMARY KEY NOT NULL,
 version TEXT NOT NULL,
 name TEXT NOT NULL,
 settings TEXT NOT NULL CHECK (json_valid(settings) AND json_type(settings) = 'object' AND length(settings) <= 65536)
) STRICT;
CREATE TABLE effects (
 package_id TEXT NOT NULL REFERENCES packages(id) ON DELETE CASCADE,
 id TEXT NOT NULL,
 enabled INTEGER NOT NULL CHECK (enabled IN (0,1)),
 cooldown_seconds INTEGER NOT NULL CHECK (cooldown_seconds BETWEEN 0 AND 86400),
 busy_behavior TEXT NOT NULL CHECK (busy_behavior IN ('drop','queue')),
 queue_limit INTEGER NOT NULL CHECK (queue_limit BETWEEN 0 AND 100),
 allowed_roles TEXT NOT NULL CHECK (json_valid(allowed_roles) AND json_type(allowed_roles) = 'array'),
 triggers TEXT NOT NULL CHECK (json_valid(triggers) AND json_type(triggers) = 'array' AND json_array_length(triggers) <= 100),
 PRIMARY KEY (package_id, id)
) STRICT;
`}
