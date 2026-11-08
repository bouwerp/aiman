package gateway

import "errors"

// ErrInsideSession is returned when the gateway is started inside a session's
// terminal. Serve classifies callers by their process tree, so a gateway there
// would have its device calls read as that session's calls; it does not run
// there at all.
var ErrInsideSession = errors.New("aiman-gateway refuses to serve from inside a session's terminal: start it from the operator's shell or its systemd unit")

// RefuseInsideSession returns ErrInsideSession when inHolderTree reports that
// this process runs inside a pty holder's tree, else nil.
func RefuseInsideSession(inHolderTree func() bool) error {
	if inHolderTree() {
		return ErrInsideSession
	}
	return nil
}
