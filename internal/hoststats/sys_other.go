//go:build !linux

package hoststats

import "io/fs"

func fileUID(fs.FileInfo) (uint32, bool) { return 0, false }

func diskSpace(string) (total, used uint64, ok bool) { return 0, 0, false }
