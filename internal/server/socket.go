package server

import (
	"errors"
	"net"
	"os"
	"path/filepath"
)

const (
	sockFile = "aiman.sock"
	lockFile = "aiman.sock.lock"
)

var (
	ErrAlreadyRunning   = errors.New(CodeAlreadyRunning)
	ErrServerNotRunning = errors.New(CodeServerNotRunning)
)

type Listener struct {
	*net.UnixListener
	lock *os.File
	path string
}

func SocketPath(dir string) string {
	return filepath.Join(dir, sockFile)
}
