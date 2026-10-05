//go:build darwin

package fsx

import (
	"io/fs"
	"syscall"
	"time"
)

// statOf extracts the device, inode and link count from a FileInfo returned by
// os.Lstat or DirEntry.Info.
func statOf(fi fs.FileInfo) (st statInfo, ok bool) {
	s, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return statInfo{}, false
	}
	return statInfo{dev: uint64(s.Dev), ino: s.Ino, nlink: uint64(s.Nlink)}, true
}

// AccessTime returns the last access time recorded for a file, when the
// platform reports one. Filesystems mounted with noatime update it rarely, so
// callers use it only together with the modification time.
func AccessTime(fi fs.FileInfo) (time.Time, bool) {
	s, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(s.Atimespec.Unix()), true
}

// openDirFlags makes opening a directory fail instead of following a symbolic
// link that replaced it after it was inspected.
const openDirFlags = syscall.O_NOFOLLOW | syscall.O_DIRECTORY
