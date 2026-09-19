package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func checkPrivateDACL(t *testing.T, path string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount != 1 {
		t.Fatalf("expected one user ACE: %v %v", acl, err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	text := sd.String()
	if !strings.Contains(text, ";;;"+user.User.Sid.String()+")") || !strings.Contains(text, ";FA;;;") {
		t.Fatalf("unexpected DACL: %s", text)
	}
}
func TestPrivateWindowsDACLIncludingJournal(t *testing.T) {
	s, dir := testStore(t)
	checkPrivateDACL(t, dir)
	checkPrivateDACL(t, filepath.Join(dir, "settings.db"))
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE app_settings SET overlay_port=20000"); err != nil {
		t.Fatal(err)
	}
	checkPrivateDACL(t, filepath.Join(dir, "settings.db-journal"))
}
func TestTightenExistingWindowsDACL(t *testing.T) {
	s, dir := testStore(t)
	s.Close()
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	s, err = openAt(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	checkPrivateDACL(t, dir)
	checkPrivateDACL(t, filepath.Join(dir, "settings.db"))
}

// Exercise creation separately so an ownership regression is distinguishable
// from a SQLite open failure, including under elevated Windows CI tokens.
func TestWindowsCreationUsesCurrentUserOwner(t *testing.T) {
	sd, user, err := userDescriptor()
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(user) {
		t.Fatal("security descriptor must explicitly name the current user as owner")
	}
	dir := filepath.Join(t.TempDir(), "owned")
	if err := secureDirectory(dir); err != nil {
		t.Fatalf("create private directory: %v", err)
	}
	path := filepath.Join(dir, "settings.db")
	if err := createPrivateFile(path); err != nil {
		t.Fatalf("create private file: %v", err)
	}
	for _, p := range []string{dir, path} {
		actual, err := windows.GetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		owner, _, err := actual.Owner()
		if err != nil || owner == nil || !owner.Equals(user) {
			t.Fatal("created object must be owned by the current user")
		}
		checkPrivateDACL(t, p)
	}
	if err := secureFile(path); err != nil {
		t.Fatalf("verify created file: %v", err)
	}
}
