package cliche

import "golang.org/x/sys/windows"

// Windows reports these where POSIX has ENOTDIR and ENOTEMPTY, and Go maps neither to a POSIX errno.
var platformCodes = []systemCode{
	{windows.ERROR_DIRECTORY, "ENOTDIR", "not a directory"},
	{windows.ERROR_DIR_NOT_EMPTY, "ENOTEMPTY", "directory not empty"},
}
