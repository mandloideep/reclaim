//go:build !darwin && !linux

package fsx

import "io/fs"

// statOf is unsupported on this platform. Without device ids the walker cannot
// detect filesystem boundaries or hard links, so every file counts.
func statOf(fs.FileInfo) (st statInfo, ok bool) { return statInfo{}, false }

// openDirFlags is empty on platforms without O_NOFOLLOW support.
const openDirFlags = 0
