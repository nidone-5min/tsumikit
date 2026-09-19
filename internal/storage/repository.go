package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// AppSettings contains only non-secret, application-wide preferences.
type AppSettings struct{ OverlayPort int }

func DefaultAppSettings() AppSettings { return AppSettings{OverlayPort: 18500} }

// AppSettings returns safe defaults alongside any error; callers must surface
// the error and must not automatically save defaults over unreadable data.
func (s *Store) AppSettings(ctx context.Context) (AppSettings, error) {
	settings := DefaultAppSettings()
	var port int
	err := s.db.QueryRowContext(ctx, "SELECT overlay_port FROM app_settings WHERE id=1").Scan(&port)
	if err != nil {
		return settings, storageError(err)
	}
	if port < 1024 || port > 65535 {
		return settings, ErrInvalid
	}
	return AppSettings{OverlayPort: port}, nil
}
func (s *Store) SaveAppSettings(ctx context.Context, settings AppSettings) error {
	if settings.OverlayPort < 1024 || settings.OverlayPort > 65535 {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO app_settings(id,overlay_port) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET overlay_port=excluded.overlay_port", settings.OverlayPort)
	return storageError(err)
}

// StreamInput is isolated by provider and authenticated account, not stream URL.
// AccountID is a provider's stable ID, never a token or a display name.
type StreamInput struct{ Platform, AccountID, URL string }

var youtubeID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
var twitchName = regexp.MustCompile(`^[A-Za-z0-9_]{1,25}$`)
var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func validScope(platform, account string) bool {
	return (platform == "youtube" || platform == "twitch") && len(account) <= 256 && identifier.MatchString(account)
}

// Canonicalize known public URLs; reject credentials, fragments, extra query
// values, and arbitrary hosts instead of persisting possible bearer secrets.
func canonicalStreamURL(platform, raw string) (string, bool) {
	if len(raw) > 2048 {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.RawFragment != "" || u.Opaque != "" {
		return "", false
	}
	if platform == "twitch" && (u.Host == "www.twitch.tv" || u.Host == "twitch.tv") && u.RawQuery == "" {
		name := strings.TrimPrefix(u.Path, "/")
		if twitchName.MatchString(name) {
			return "https://www.twitch.tv/" + strings.ToLower(name), true
		}
	}
	if platform != "youtube" {
		return "", false
	}
	id := ""
	switch u.Host {
	case "www.youtube.com", "youtube.com":
		if u.Path == "/watch" {
			q, err := url.ParseQuery(u.RawQuery)
			if err != nil || len(q) != 1 || len(q["v"]) != 1 {
				return "", false
			}
			id = q.Get("v")
		} else if strings.HasPrefix(u.Path, "/live/") && u.RawQuery == "" {
			id = strings.TrimPrefix(u.Path, "/live/")
		}
	case "youtu.be":
		if u.RawQuery == "" {
			id = strings.TrimPrefix(u.Path, "/")
		}
	}
	if !youtubeID.MatchString(id) {
		return "", false
	}
	return "https://www.youtube.com/watch?v=" + id, true
}
func (s *Store) SaveStreamInput(ctx context.Context, input StreamInput) error {
	canonical, ok := canonicalStreamURL(input.Platform, input.URL)
	if !validScope(input.Platform, input.AccountID) || !ok {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO stream_inputs(platform,account_id,stream_url) VALUES(?,?,?) ON CONFLICT(platform,account_id) DO UPDATE SET stream_url=excluded.stream_url", input.Platform, input.AccountID, canonical)
	return storageError(err)
}
func (s *Store) StreamInput(ctx context.Context, platform, account string) (StreamInput, error) {
	if !validScope(platform, account) {
		return StreamInput{}, ErrInvalid
	}
	input := StreamInput{Platform: platform, AccountID: account}
	err := s.db.QueryRowContext(ctx, "SELECT stream_url FROM stream_inputs WHERE platform=? AND account_id=?", platform, account).Scan(&input.URL)
	if err != nil {
		return StreamInput{}, storageError(err)
	}
	canonical, ok := canonicalStreamURL(platform, input.URL)
	if !ok {
		return StreamInput{}, ErrInvalid
	}
	input.URL = canonical
	return input, nil
}
func (s *Store) DeleteStreamInput(ctx context.Context, platform, account string) error {
	if !validScope(platform, account) {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM stream_inputs WHERE platform=? AND account_id=?", platform, account)
	return storageError(err)
}

// PackageConfig is configuration only: ZIP bytes, paths, capability URLs and
// runtime state are not stored here. Settings and trigger object semantics must
// also be validated by the future manifest/condition layer before saving.
type PackageConfig struct {
	ID       string
	Version  string
	Name     string
	Settings json.RawMessage
	Effects  []EffectConfig
}
type EffectConfig struct {
	ID              string
	Enabled         bool
	CooldownSeconds int
	BusyBehavior    string
	QueueLimit      int
	AllowedRoles    []string
	Triggers        []json.RawMessage
}

func validText(value string, max int) bool {
	return len(value) > 0 && len(value) <= max && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}
func validObject(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return len(raw) <= 65536 && utf8.Valid(raw) && len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(raw)
}
func validPackage(p PackageConfig) bool {
	if !identifier.MatchString(p.ID) || !validText(p.Version, 128) || !validText(p.Name, 256) || !validObject(p.Settings) || len(p.Effects) > 100 {
		return false
	}
	seen := make(map[string]bool)
	total := len(p.Settings)
	for _, e := range p.Effects {
		if !identifier.MatchString(e.ID) || seen[e.ID] || e.CooldownSeconds < 0 || e.CooldownSeconds > 86400 || (e.BusyBehavior != "drop" && e.BusyBehavior != "queue") || e.QueueLimit < 0 || e.QueueLimit > 100 || len(e.Triggers) > 100 || len(e.AllowedRoles) > 16 {
			return false
		}
		seen[e.ID] = true
		roles := make(map[string]bool)
		for _, role := range e.AllowedRoles {
			switch role {
			case "owner", "moderator", "member", "viewer":
			default:
				return false
			}
			if roles[role] {
				return false
			}
			roles[role] = true
		}
		for _, trigger := range e.Triggers {
			if !validObject(trigger) {
				return false
			}
			total += len(trigger)
			if total > 1048576 {
				return false
			}
		}
	}
	return true
}

// SavePackage atomically replaces one package's metadata and configuration.
// The old version remains intact if any write, cancellation or commit fails.
func (s *Store) SavePackage(ctx context.Context, p PackageConfig) error {
	if !validPackage(p) {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError(err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, "INSERT INTO packages(id,version,name,settings) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET version=excluded.version,name=excluded.name,settings=excluded.settings", p.ID, p.Version, p.Name, string(p.Settings))
	if err != nil {
		return storageError(err)
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM effects WHERE package_id=?", p.ID); err != nil {
		return storageError(err)
	}
	for _, e := range p.Effects {
		roles := e.AllowedRoles
		if roles == nil {
			roles = []string{}
		}
		triggers := e.Triggers
		if triggers == nil {
			triggers = []json.RawMessage{}
		}
		roleJSON, _ := json.Marshal(roles)
		triggerJSON, err := json.Marshal(triggers)
		if err != nil {
			return ErrInvalid
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO effects(package_id,id,enabled,cooldown_seconds,busy_behavior,queue_limit,allowed_roles,triggers) VALUES(?,?,?,?,?,?,?,?)", p.ID, e.ID, e.Enabled, e.CooldownSeconds, e.BusyBehavior, e.QueueLimit, string(roleJSON), string(triggerJSON))
		if err != nil {
			return storageError(err)
		}
	}
	return storageError(tx.Commit())
}

// Package reads a consistent snapshot, including its effects, across writers.
func (s *Store) Package(ctx context.Context, id string) (PackageConfig, error) {
	if !identifier.MatchString(id) {
		return PackageConfig{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PackageConfig{}, storageError(err)
	}
	defer tx.Rollback()
	p := PackageConfig{ID: id, Effects: []EffectConfig{}}
	var settings string
	err = tx.QueryRowContext(ctx, "SELECT version,name,settings FROM packages WHERE id=?", id).Scan(&p.Version, &p.Name, &settings)
	if err != nil {
		return PackageConfig{}, storageError(err)
	}
	p.Settings = json.RawMessage(settings)
	rows, err := tx.QueryContext(ctx, "SELECT id,enabled,cooldown_seconds,busy_behavior,queue_limit,allowed_roles,triggers FROM effects WHERE package_id=? ORDER BY id", id)
	if err != nil {
		return PackageConfig{}, storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var e EffectConfig
		var roles, triggers string
		if err = rows.Scan(&e.ID, &e.Enabled, &e.CooldownSeconds, &e.BusyBehavior, &e.QueueLimit, &roles, &triggers); err != nil {
			return PackageConfig{}, storageError(err)
		}
		if json.Unmarshal([]byte(roles), &e.AllowedRoles) != nil || json.Unmarshal([]byte(triggers), &e.Triggers) != nil {
			return PackageConfig{}, ErrInvalid
		}
		p.Effects = append(p.Effects, e)
	}
	if err = rows.Err(); err != nil {
		return PackageConfig{}, storageError(err)
	}
	if !validPackage(p) {
		return PackageConfig{}, ErrInvalid
	}
	if err = tx.Commit(); err != nil {
		return PackageConfig{}, storageError(err)
	}
	return p, nil
}
func (s *Store) PackageIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM packages ORDER BY id")
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, storageError(err)
		}
		if !identifier.MatchString(id) {
			return nil, ErrInvalid
		}
		ids = append(ids, id)
	}
	return ids, storageError(rows.Err())
}
func (s *Store) DeletePackage(ctx context.Context, id string) error {
	if !identifier.MatchString(id) {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM packages WHERE id=?", id)
	return storageError(err)
}
