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
