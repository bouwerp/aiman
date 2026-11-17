package cgroup

import "path/filepath"

// ReadHeld is Usage.Held of the cgroup at dir: anon plus shmem from
// memory.stat, else memory.current. ok is false when dir has no
// memory.current, such as a cgroup that is gone.
func ReadHeld(dir string) (int64, bool) {
	cur, ok := readValue(dir, "memory.current")
	if !ok {
		return 0, false
	}
	stat := readKeyed(filepath.Join(dir, "memory.stat"))
	return Usage{MemoryCurrent: cur, Anon: stat["anon"], Shmem: stat["shmem"]}.Held(), true
}

// ReadHigh is dir's memory.high: Infinity when it has none, and ok false when
// it cannot be read.
func ReadHigh(dir string) (int64, bool) {
	return readValue(dir, "memory.high")
}
