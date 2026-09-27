//go:build windows

package secfile

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// shareDir gives Everyone inheritable read access to dir, like a shared
// Downloads folder.
func shareDir(t *testing.T, dir string) {
	t.Helper()
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_READ,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(everyone),
		},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
}

// Plaintext temp files must not inherit a shared destination's access.
func TestCreateTempIsOwnerOnlyInSharedDir(t *testing.T) {
	dir := t.TempDir()
	shareDir(t, dir)
	plain, err := os.CreateTemp(dir, "plain-*")
	if err != nil {
		t.Fatal(err)
	}
	plain.Close()
	if err := check(plain.Name()); err == nil {
		t.Fatal("test setup: inherited access not detected")
	}
	f, err := CreateTemp(dir, "secret-*")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := check(f.Name()); err != nil {
		t.Fatalf("CreateTemp in shared dir: %v", err)
	}
	final := filepath.Join(dir, "final")
	if err := os.Rename(f.Name(), final); err != nil {
		t.Fatal(err)
	}
	if err := check(final); err != nil {
		t.Fatalf("restriction lost after rename: %v", err)
	}
}
