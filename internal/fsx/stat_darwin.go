//go:build darwin

package fsx

import (
	"io/fs"
	"syscall"
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

// openDirFlags makes opening a directory fail instead of following a symbolic
// link that replaced it after it was inspected.
const openDirFlags = syscall.O_NOFOLLOW | syscall.O_DIRECTORY
