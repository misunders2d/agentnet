//go:build windows

package client

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/misunders2d/agentnet/internal/secfile"
	"golang.org/x/sys/windows"
)

func TestFixtureStoreRestrictsSharedHome(t *testing.T) {
	home := t.TempDir()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the owner's full control while giving other accounts inheritable
	// read access, reproducing a shared Windows temp directory.
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
		},
	}, {
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
	if err := windows.SetNamedSecurityInfo(home, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(home, "inherited-access")
	if err := os.WriteFile(probe, []byte("probe"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := secfile.Read(probe); err == nil {
		t.Fatal("test setup: inherited access not detected")
	}
	seedFixtureStore(t, home)
	_, path := paths(home)
	if _, err := secfile.Read(path); err != nil {
		t.Fatalf("fixture permissions: %v", err)
	}
	s, err := openStore(path)
	if err != nil {
		t.Fatalf("open copied store: %v", err)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
}
