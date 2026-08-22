//go:build windows

package server

import "errors"

func Listen(string) (*Listener, error) {
	return nil, errors.New("aiman serve is not supported on windows")
}

func (l *Listener) Close() error {
	if l == nil {
		return nil
	}
	if l.UnixListener != nil {
		return l.UnixListener.Close()
	}
	return nil
}
