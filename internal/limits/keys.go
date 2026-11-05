package limits

import "net/http"

// newTransport is the transport for provider calls. Stub until P2.
func newTransport() *http.Transport {
	t, _ := http.DefaultTransport.(*http.Transport)
	return t
}
