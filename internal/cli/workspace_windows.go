package cli

import (
	"strings"

	"golang.org/x/sys/windows"
)

// resolveExistingPath resolves an existing Windows path through its file handle, following
// junctions and symbolic links.
func resolveExistingPath(path string) (string, error) {
	// Extended names keep long paths usable. Opening without
	// FILE_FLAG_OPEN_REPARSE_POINT follows junctions and symlinks to the target.
	handlePath := path
	if !strings.HasPrefix(handlePath, `\\?\`) {
		if strings.HasPrefix(handlePath, `\\`) {
			handlePath = `\\?\UNC\` + strings.TrimPrefix(handlePath, `\\`)
		} else {
			handlePath = `\\?\` + handlePath
		}
	}
	name, err := windows.UTF16PtrFromString(handlePath)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	buffer := make([]uint16, 260)
	for {
		// Zero flags request FILE_NAME_NORMALIZED and VOLUME_NAME_DOS.
		length, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
		if err != nil {
			return "", err
		}
		if length < uint32(len(buffer)) {
			return normalizeWindowsFinalPath(windows.UTF16ToString(buffer[:length]))
		}
		buffer = make([]uint16, length+1)
	}
}
