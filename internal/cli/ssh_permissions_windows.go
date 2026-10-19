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
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read SSH owner for %q: %w", path, err)
	}
	if descriptor == nil {
		return fmt.Errorf("SSH path %q has no security descriptor", path)
	}
	previousOwner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("read SSH owner for %q: %w", path, err)
	}
	if previousOwner == nil {
		return fmt.Errorf("SSH path %q has no owner", path)
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
	// An owner can change the DACL without WRITE_OWNER. Native ssh-keygen
	// files grant read/write access, so redundantly assigning their existing
	// owner would fail. Protection still excludes inherited access for others.
	information := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
	var owner *windows.SID
	if !previousOwner.Equals(user.User.Sid) {
		information |= windows.OWNER_SECURITY_INFORMATION
		owner = user.User.Sid
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, information, owner, nil, dacl, nil); err != nil {
		return fmt.Errorf("secure SSH path %q: %w", path, err)
	}
	return nil
}

// secureSSHReplacement preserves an existing user configuration DACL on a
// temporary replacement, or applies managed permissions when no file existed.
func secureSSHReplacement(path string, mode os.FileMode, previous string) error {
	if previous == "" {
		return secureSSHPath(path, mode)
	}
	descriptor, err := windows.GetNamedSecurityInfo(previous, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read SSH permissions from %q: %w", previous, err)
	}
	if descriptor == nil {
		return fmt.Errorf("SSH config %q has no security descriptor", previous)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("read process user for SSH permissions: %w", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read SSH DACL from %q: %w", previous, err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return fmt.Errorf("read SSH permission control from %q: %w", previous, err)
	}
	// Assign the process user as owner, which needs no administrator privilege.
	// Copying an existing owner such as SYSTEM or Administrators may require
	// privileges a standard account does not have.
	information := windows.SECURITY_INFORMATION(windows.OWNER_SECURITY_INFORMATION | windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
	if control&windows.SE_DACL_PROTECTED != 0 {
		information = windows.OWNER_SECURITY_INFORMATION | windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, information, user.User.Sid, nil, dacl, nil); err != nil {
		return fmt.Errorf("preserve SSH permissions on %q: %w", path, err)
	}
	return nil
}
