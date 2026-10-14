package cli

import (
	"fmt"
	"os"
	"runtime"

	"golang.org/x/sys/windows"
)

// secureSSHPath replaces inherited permissions with access for the process user.
// The caller limits this to managed paths and user SSH paths it just created.
func secureSSHPath(path string, mode os.FileMode) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("read process user for SSH permissions: %w", err)
	}
	// TRUSTEE stores its SID as a uintptr, so keep the SID pinned until the
	// Windows calls have finished using it.
	var pinner runtime.Pinner
	pinner.Pin(user.User.Sid)
	defer pinner.Unpin()
	inheritance := uint32(windows.NO_INHERITANCE)
	if mode.IsDir() {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	dacl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
		},
	}}, nil)
	if err != nil {
		return fmt.Errorf("build SSH permissions for %q: %w", path, err)
	}
	// Setting an owner from the current token and changing our own files'
	// DACLs requires no administrator privilege. Protection prevents parent
	// permissions from granting access to additional users.
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		user.User.Sid, nil, dacl, nil); err != nil {
		return fmt.Errorf("secure SSH path %q: %w", path, err)
	}
	return nil
}
