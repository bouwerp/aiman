package console

import (
	"context"

	"github.com/bouwerp/aiman/internal/usecase"
)

// HostForwarder is the Forwarder of a console that runs on the harness host
// itself: each relay port is already on the host's loopback, so a forward is
// 127.0.0.1:<host port> and nothing is opened.
type HostForwarder struct{}

// Open maps each port to itself.
func (HostForwarder) Open(_ context.Context, ports []usecase.HarnessPort) (Forwards, error) {
	fw := make([]usecase.HarnessForward, 0, len(ports))
	for _, p := range ports {
		fw = append(fw, usecase.HarnessForward{Name: p.Name, LocalPort: p.HostPort, HostPort: p.HostPort})
	}
	return hostForwards{fwds: fw, died: make(chan usecase.HarnessForward)}, nil
}

// hostForwards never dies: there is no child to exit.
type hostForwards struct {
	fwds []usecase.HarnessForward
	died chan usecase.HarnessForward
}

func (h hostForwards) Forwards() []usecase.HarnessForward  { return h.fwds }
func (h hostForwards) Died() <-chan usecase.HarnessForward { return h.died }
func (hostForwards) Close()                                {}
