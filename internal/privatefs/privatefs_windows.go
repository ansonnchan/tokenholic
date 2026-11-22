package privatefs

import (
	"errors"
	"fmt"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ApplicationRoot returns the directory tokenholic's own directory lives in.
// On Windows that is the local, non-roaming application data folder: the
// database and its upload state belong to one device and must not follow a
// roaming profile to another.
func ApplicationRoot() (string, error) {
	root, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return "", fmt.Errorf("resolve local application data folder: %w", err)
	}
	if !filepath.IsAbs(root) {
		return "", errors.New("resolve local application data folder: not an absolute path")
	}
	return root, nil
}

func currentUser() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("identify the current user: %w", err)
	}
	return user.User.Sid, nil
}

// restrict replaces the access-control list with one entry granting the
// current user full control, and protects it so nothing is inherited from
// the parent. A directory passes the entry on to what is created inside it,
// so SQLite's journal files and later logs are private from birth.
func restrict(path string, directory bool) error {
	user, err := currentUser()
	if err != nil {
		return err
	}
	var inheritance uint32 = windows.NO_INHERITANCE
	if directory {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	list, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user),
		},
	}}, nil)
	if err != nil {
		return fmt.Errorf("build access-control list: %w", err)
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, list, nil)
}

// IsPrivate reports whether path's access-control list is protected from
// inheritance and grants access to the current user and nobody else.
func IsPrivate(path string) (bool, error) {
	user, err := currentUser()
	if err != nil {
		return false, err
	}
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, err
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return false, err
	}
	list, _, err := descriptor.DACL()
	if err != nil {
		return false, err
	}
	// A missing list grants everyone access.
	if list == nil || control&windows.SE_DACL_PROTECTED == 0 || list.AceCount == 0 {
		return false, nil
	}
	for index := uint32(0); index < uint32(list.AceCount); index++ {
		var entry *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(list, index, &entry); err != nil {
			return false, err
		}
		if entry.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return false, nil
		}
		if !(*windows.SID)(unsafe.Pointer(&entry.SidStart)).Equals(user) {
			return false, nil
		}
	}
	return true, nil
}
