package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "app")
	s, err := openAt(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}
func fixture(id string) PackageConfig {
	return PackageConfig{ID: id, Version: "1.0.0", Name: "乾杯", Settings: json.RawMessage(`{"color":"red"}`), Effects: []EffectConfig{{ID: "toast", Enabled: true, CooldownSeconds: 10, BusyBehavior: "queue", QueueLimit: 5, AllowedRoles: []string{"viewer", "member"}, Triggers: []json.RawMessage{json.RawMessage(`{"word":"乾杯"}`)}}}}
}
func TestReopenAndCRUD(t *testing.T) {
	ctx := context.Background()
	s, dir := testStore(t)
	if got, err := s.AppSettings(ctx); err != nil || got != DefaultAppSettings() {
		t.Fatalf("defaults: %v %v", got, err)
	}
	if err := s.SaveAppSettings(ctx, AppSettings{20000}); err != nil {
		t.Fatal(err)
	}
	p := fixture("sample")
	if err := s.SavePackage(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePackage(ctx, fixture("other")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := openAt(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Package(ctx, p.ID)
	if err != nil || !reflect.DeepEqual(got, p) {
		t.Fatalf("roundtrip: %#v %v", got, err)
	}
	if settings, err := s.AppSettings(ctx); err != nil || settings.OverlayPort != 20000 {
		t.Fatalf("settings: %v %v", settings, err)
	}
	p.Version = "2"
	p.Effects = nil
	if err := s.SavePackage(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err = s.Package(ctx, p.ID)
	if err != nil || got.Version != "2" || len(got.Effects) != 0 {
		t.Fatalf("replacement: %v %v", got, err)
	}
	if err := s.DeletePackage(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Package(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	ids, err := s.PackageIDs(ctx)
	if err != nil || !reflect.DeepEqual(ids, []string{"other"}) {
		t.Fatalf("ids: %v %v", ids, err)
	}
	if err := s.DeletePackage(ctx, "other"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM effects").Scan(&count); err != nil || count != 0 {
		t.Fatalf("cascade: %v %v", count, err)
	}
}
func TestStreamInputsIsolatedAndCanonical(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	inputs := []StreamInput{{"youtube", "a", "https://youtu.be/abcdefghijk"}, {"youtube", "b", "https://www.youtube.com/live/12345678901"}, {"twitch", "a", "https://twitch.tv/Example"}}
	for _, input := range inputs {
		if err := s.SaveStreamInput(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	for i, input := range inputs {
		got, err := s.StreamInput(ctx, input.Platform, input.AccountID)
		canonical, _ := canonicalStreamURL(input.Platform, input.URL)
		if err != nil || got.URL != canonical {
			t.Fatalf("%d: %v %v", i, got, err)
		}
	}
	if err := s.DeleteStreamInput(ctx, "youtube", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StreamInput(ctx, "youtube", "a"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.StreamInput(ctx, "twitch", "a"); err != nil {
		t.Fatal(err)
	}
	other, _ := testStore(t)
	if _, err := other.StreamInput(ctx, "twitch", "a"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	for _, raw := range []string{"https://twitch.tv/example?access_token=secret", "https://user:secret@twitch.tv/example", "https://twitch.tv.evil/example", "http://twitch.tv/example", "https://twitch.tv/example#secret", "https://twitch.tv:443/example", "https://twitch.tv/a/b"} {
		if err := s.SaveStreamInput(ctx, StreamInput{"twitch", "a", raw}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, raw := range []string{"https://youtu.be/abcdefghijk?token=secret", "https://youtube.com/watch?v=abcdefghijk&v=12345678901", "https://youtube.com/watch?v=abcdefghijk&token=secret"} {
		if err := s.SaveStreamInput(ctx, StreamInput{"youtube", "a", raw}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %s", raw)
		}
	}
}
func TestMigrationRollbackAndUpgrade(t *testing.T) {
	ctx := context.Background()
	s, dir := testStore(t)
	steps := append(append([]string{}, migrations...), "CREATE TABLE future_data (id INTEGER); INSERT INTO future_data VALUES(42);", "CREATE TABLE never_committed (id INTEGER); INVALID SQL")
	if err := s.migrate(ctx, steps); !errors.Is(err, ErrStorage) {
		t.Fatal(err)
	}
	var version, count int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatalf("version: %d %v", version, err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name IN ('future_data','never_committed')").Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial migration: %d %v", count, err)
	}
	if err := s.migrate(ctx, steps[:2]); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(ctx, steps[:2]); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT id FROM future_data").Scan(&count); err != nil || count != 42 {
		t.Fatalf("upgrade: %d %v", count, err)
	}
	s.Close()
	before, err := os.ReadFile(filepath.Join(dir, "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openAt(ctx, dir); !errors.Is(err, ErrFutureSchema) {
		t.Fatalf("future schema: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "settings.db"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("future DB modified", err)
	}
}
func TestPackageTransactionRollback(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	p := fixture("sample")
	if err := s.SavePackage(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_effect BEFORE INSERT ON effects WHEN NEW.id='fail' BEGIN SELECT RAISE(ABORT,'private-value'); END;`); err != nil {
		t.Fatal(err)
	}
	update := fixture("sample")
	update.Name = "changed"
	update.Effects = append(update.Effects, EffectConfig{ID: "fail", BusyBehavior: "drop"})
	if err := s.SavePackage(ctx, update); !errors.Is(err, ErrStorage) || strings.Contains(err.Error(), "private-value") {
		t.Fatal(err)
	}
	got, err := s.Package(ctx, p.ID)
	if err != nil || !reflect.DeepEqual(got, p) {
		t.Fatalf("partial write: %v %v", got, err)
	}
}
func TestInvalidConfigurationDoesNotOverwrite(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	for _, port := range []int{-1, 0, 1023, 65536} {
		if err := s.SaveAppSettings(ctx, AppSettings{port}); !errors.Is(err, ErrInvalid) {
			t.Fatal(port, err)
		}
	}
	for _, port := range []int{1024, 65535} {
		if err := s.SaveAppSettings(ctx, AppSettings{port}); err != nil {
			t.Fatal(port, err)
		}
	}
	cases := []func(*PackageConfig){func(p *PackageConfig) { p.ID = "../escape" }, func(p *PackageConfig) { p.Settings = json.RawMessage(`[]`) }, func(p *PackageConfig) { p.Settings = json.RawMessage(`{"x":"` + strings.Repeat("x", 65536) + `"}`) }, func(p *PackageConfig) { p.Effects[0].CooldownSeconds = 86401 }, func(p *PackageConfig) { p.Effects[0].QueueLimit = 101 }, func(p *PackageConfig) { p.Effects[0].BusyBehavior = "other" }, func(p *PackageConfig) { p.Effects[0].AllowedRoles = []string{"unknown"} }, func(p *PackageConfig) { p.Effects = append(p.Effects, p.Effects[0]) }, func(p *PackageConfig) { p.Effects[0].Triggers = []json.RawMessage{json.RawMessage(`null`)} }, func(p *PackageConfig) { p.Effects[0].Triggers = make([]json.RawMessage, 101) }}
	for i, mutate := range cases {
		p := fixture("sample")
		mutate(&p)
		if err := s.SavePackage(ctx, p); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%d: %v", i, err)
		}
	}
}
func TestCorruptFilePreservedAndReadDefaults(t *testing.T) {
	ctx := context.Background()
	s, dir := testStore(t)
	if _, err := s.db.Exec("DROP TABLE app_settings"); err != nil {
		t.Fatal(err)
	}
	settings, err := s.AppSettings(ctx)
	if !errors.Is(err, ErrStorage) || settings != DefaultAppSettings() {
		t.Fatalf("fallback: %v %v", settings, err)
	}
	s.Close()
	corrupt := []byte("unreadable existing data")
	path := filepath.Join(dir, "settings.db")
	if err := os.WriteFile(path, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openAt(ctx, dir); !errors.Is(err, ErrStorage) {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, corrupt) {
		t.Fatal("corrupt DB overwritten", err)
	}
}
func TestConcurrentStoresCancellationAndClose(t *testing.T) {
	ctx := context.Background()
	s, dir := testStore(t)
	second, err := openAt(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := s
			if i%2 != 0 {
				store = second
			}
			for j := 0; j < 5; j++ {
				p := fixture(fmt.Sprintf("p%d", i))
				p.Version = fmt.Sprint(j)
				if err := store.SavePackage(ctx, p); err != nil {
					t.Error(err)
					return
				}
				got, err := store.Package(ctx, p.ID)
				if err != nil || got.Version != p.Version {
					t.Errorf("snapshot: %v %v", got, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.SavePackage(canceled, fixture("canceled")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// A held connection makes the next operation wait; cancellation must release it.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline, done := context.WithTimeout(ctx, 20*time.Millisecond)
	defer done()
	if err := s.SaveAppSettings(deadline, AppSettings{20001}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	tx.Rollback()
	if _, err := s.Package(ctx, "canceled"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if settings, err := s.AppSettings(ctx); !errors.Is(err, ErrStorage) || settings != DefaultAppSettings() {
		t.Fatal(settings, err)
	}
}
func TestRejectLinksAndSpecialPaths(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlink unavailable:", err)
	}
	if _, err := openAt(ctx, link); !errors.Is(err, ErrStorage) {
		t.Fatal("directory link accepted", err)
	}
	for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
		t.Run(suffix, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "app")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(dir, "settings.db"+suffix)); err != nil {
				t.Fatal(err)
			}
			if _, err := openAt(ctx, dir); !errors.Is(err, ErrStorage) {
				t.Fatal("file link accepted", err)
			}
		})
	}
}
func TestRejectHardlink(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "app")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "target")
	content := []byte("must remain unchanged")
	if err := os.WriteFile(target, content, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, filepath.Join(dir, "settings.db")); err != nil {
		t.Skip(err)
	}
	if _, err := openAt(context.Background(), dir); !errors.Is(err, ErrStorage) {
		t.Fatal("hardlink accepted", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatal("target modified", err)
	}
}
