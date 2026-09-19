//go:build darwin || linux

package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivatePermissionsIncludingJournal(t *testing.T) {
	s, dir := testStore(t)
	for path, mode := range map[string]os.FileMode{dir: 0700, filepath.Join(dir, "settings.db"): 0600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("permission: %v %v", info, err)
		}
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE app_settings SET overlay_port=20000"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "settings.db-journal"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("journal permission: %v %v", info, err)
	}
}
func TestTightenExistingPermissions(t *testing.T) {
	s, dir := testStore(t)
	s.Close()
	path := filepath.Join(dir, "settings.db")
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	s, err := openAt(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for p, mode := range map[string]os.FileMode{dir: 0700, path: 0600} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("permission: %v %v", info, err)
		}
	}
}
