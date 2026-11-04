package hoststats

import (
	"io/fs"
	"syscall"
)

func fileUID(info fs.FileInfo) (uint32, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
}

func diskSpace(path string) (total, used uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, false
	}
	bsize := uint64(st.Bsize) // #nosec G115 -- a block size is positive
	return st.Blocks * bsize, (st.Blocks - st.Bfree) * bsize, true
}
