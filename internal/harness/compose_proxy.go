package harness

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/bouwerp/aiman/internal/harness/edge"
)

// Capture proxy names and paths in a run's project.
const (
	// CAVolume is the in-memory volume the edge writes the run CA's
	// certificate to and every other container mounts read-only at CAMount.
	CAVolume = "aiman-ca"
	CAMount  = "/aiman-ca"
	// CACertPath is the certificate inside the containers.
	CACertPath = CAMount + "/" + edge.CACertName
	// ProxyURL is the proxy the app and services are told to use.
	ProxyURL = "http://" + ServiceEdge + ":3128"
	// StubsFile is the scenario's stubs in the control directory, which only
	// the edge mounts.
	StubsFile = "stubs.json"
	// edgeRootsPath is where an online edge sees the host's CA bundle.
	edgeRootsPath = "/aiman-roots.pem"
	// caVolumeSize bounds the CA volume.
	caVolumeSize = "1m"
)

// ProxySpec turns on the edge's capture proxy for a run.
type ProxySpec struct {
	// Online forwards requests no stub answers; offline answers them 502.
	Online bool
	// BodyBytes is harness.capture.body_bytes; zero is the edge's default.
	BodyBytes int
	// UpstreamRoots is the host's CA bundle, mounted read-only in an online
	// edge to verify upstream servers; required online.
	UpstreamRoots string
	// Stubs tells the edge to read StubsFile from the control directory.
	Stubs bool
}

// Network is the run's network mode as run.json records it.
func (p *ProxySpec) Network() string {
	if p != nil && p.Online {
		return "online"
	}
	return "offline"
}

// NoProxy is the NO_PROXY of a run: loopback, the app, the edge, every
// service of the scenario, the debug adapter in a debug run, and the inner
// subnet, so traffic between containers never goes through the proxy.
func NoProxy(sc *Scenario, spec RunSpec) string {
	names := []string{"localhost", "127.0.0.1", ServiceApp, ServiceEdge}
	names = append(names, sc.Services...)
	if spec.Debug != nil && spec.Debug.Adapter != nil {
		names = append(names, ServiceAdapter)
	}
	if spec.InnerSubnet.IsValid() {
		names = append(names, spec.InnerSubnet.Masked().String())
	}
	return strings.Join(names, ",")
}

// proxyEnv is what the app and services read to use the proxy and trust the
// run CA. Both spellings of the proxy variables are set: curl reads only the
// lower case http_proxy.
func proxyEnv(noProxy string) map[string]string {
	return map[string]string{
		"HTTP_PROXY":          ProxyURL,
		"HTTPS_PROXY":         ProxyURL,
		"http_proxy":          ProxyURL,
		"https_proxy":         ProxyURL,
		"NO_PROXY":            noProxy,
		"no_proxy":            noProxy,
		"NODE_USE_ENV_PROXY":  "1",
		"SSL_CERT_FILE":       CACertPath,
		"NODE_EXTRA_CA_CERTS": CACertPath,
		"REQUESTS_CA_BUNDLE":  CACertPath,
	}
}

// withProxy adds the capture proxy to a run's project: the edge listens on
// its inner address, keeps the CA in memory and writes its certificate to
// CAVolume; the app gets the proxy variables and the certificate, and so
// does every service unless its env sets a variable itself. Online, the
// outer network masquerades so the edge, and only the edge, reaches the
// internet; the inner network stays internal. A scenario with an aws service
// has the proxy refuse every AWSBlockSuffixes host, in either mode.
func withProxy(doc *composeFile, f *File, sc *Scenario, spec RunSpec) error {
	p := spec.Proxy
	if p == nil {
		return nil
	}
	if p.Online {
		if !filepath.IsAbs(p.UpstreamRoots) || strings.ContainsAny(p.UpstreamRoots, ":,\n") {
			return errors.New("harness compose: an online run needs the host's CA bundle as an absolute path with no colon or comma")
		}
		outer := doc.Networks[NetworkOuter]
		outer.DriverOpts = nil
		doc.Networks[NetworkOuter] = outer
	}
	doc.Volumes[CAVolume] = map[string]any{
		"driver": "local",
		"driver_opts": map[string]string{
			"type":   "tmpfs",
			"device": "tmpfs",
			"o":      fmt.Sprintf("size=%s,uid=%d,gid=%d,mode=0755", caVolumeSize, spec.UID, spec.GID),
		},
	}
	env := proxyEnv(NoProxy(sc, spec))
	mount := CAVolume + ":" + CAMount + ":ro"

	app := doc.Services[ServiceApp]
	if app.Environment == nil {
		app.Environment = map[string]string{}
	}
	for k, v := range env {
		app.Environment[k] = v
	}
	app.Volumes = append(app.Volumes, mount)
	doc.Services[ServiceApp] = app

	// Checks run as the app does, with the same proxy and CA: a check in its
	// own image, a debug run's plain checks and a debug check. A debug
	// check's adapter only speaks to the check on the debug network, like
	// the app's adapter, so it gets neither.
	for name, s := range doc.Services {
		if name == ServiceCheckAdapter || !slices.ContainsFunc(s.Profiles, func(p string) bool {
			return p == checksProfile || p == profileCheck || p == profileDebugCheck
		}) {
			continue
		}
		if s.Environment == nil {
			s.Environment = map[string]string{}
		}
		for k, v := range env {
			s.Environment[k] = v
		}
		s.Volumes = append(s.Volumes, mount)
		doc.Services[name] = s
	}

	for _, name := range sc.Services {
		if f.Services[name].AWS != nil {
			continue // the emulator never goes out
		}
		s := doc.Services[name]
		if s.Environment == nil {
			s.Environment = map[string]string{}
		}
		for k, v := range env {
			if _, set := s.Environment[k]; !set {
				s.Environment[k] = v
			}
		}
		s.Volumes = append(s.Volumes, mount)
		doc.Services[name] = s
	}

	e := doc.Services[ServiceEdge]
	e.Command = append(e.Command,
		"--proxy", spec.InnerSubnet.Masked().String(),
		"--network", p.Network(),
		"--ca-dir", CAMount,
	)
	if p.BodyBytes > 0 {
		e.Command = append(e.Command, "--capture-body-bytes", strconv.Itoa(p.BodyBytes))
	}
	if p.Stubs {
		e.Command = append(e.Command, "--stubs", ControlMount+"/"+StubsFile)
	}
	if len(f.AWSServices(sc)) > 0 {
		for _, suffix := range AWSBlockSuffixes {
			e.Command = append(e.Command, "--block-host-suffix", suffix)
		}
	}
	e.Volumes = append(e.Volumes, CAVolume+":"+CAMount)
	if p.Online {
		e.Command = append(e.Command, "--upstream-roots", edgeRootsPath)
		e.Volumes = append(e.Volumes, p.UpstreamRoots+":"+edgeRootsPath+":ro")
	}
	doc.Services[ServiceEdge] = e
	return nil
}
