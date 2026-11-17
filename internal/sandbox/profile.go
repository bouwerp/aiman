package sandbox

import (
	"fmt"
	"strings"
)

// Hardening values every container that runs untrusted code gets.
const (
	NoNewPrivileges = "no-new-privileges:true"
	CapDropAll      = "ALL"
	// PullNever makes compose use only an image already on the host.
	PullNever = "never"
)

// CacheInitService is the compose service that hands the cache volumes to
// the workload's user. Docker creates a named volume owned by root, and the
// workload runs unprivileged with no capabilities, so it cannot do this
// itself. The service runs only chown, as root with CHOWN alone, with no
// network and without the source mounted.
const CacheInitService = "cache-init"

// Caches names the three cache volumes a workload mounts under /cache.
type Caches struct {
	GoMod, GoCache, NPM string
}

// Mounts are the cache volumes' short-syntax mounts.
func (c Caches) Mounts() []string {
	return []string{
		c.GoMod + ":/cache/gomod",
		c.GoCache + ":/cache/gocache",
		c.NPM + ":/cache/npm",
	}
}

// Volumes declares the cache volumes in a compose file.
func (c Caches) Volumes() map[string]map[string]any {
	return map[string]map[string]any{c.GoMod: {}, c.GoCache: {}, c.NPM: {}}
}

// User is the compose user uid:gid.
func User(uid, gid int) string { return fmt.Sprintf("%d:%d", uid, gid) }

// Workload is the hardened service that runs untrusted code: read-only root,
// the host uid, no capabilities, no new privileges, an executable /tmp, the
// source at /src, the output at /out and the cache volumes, started after
// CacheInitService. The caller adds the pull policy, network, command and
// limits.
func Workload(image string, uid, gid int, src, out string, c Caches) Service {
	return Service{
		Image:       image,
		WorkingDir:  "/src",
		User:        User(uid, gid),
		ReadOnly:    true,
		CapDrop:     []string{CapDropAll},
		SecurityOpt: []string{NoNewPrivileges},
		// Docker mounts a tmpfs noexec by default, and go test runs the binary
		// it builds under the temp directory. /src is already writable and
		// executable, so this gives the code under test nothing new.
		Tmpfs: []string{"/tmp:exec"},
		Environment: map[string]string{
			"HOME":             "/tmp",
			"GOMODCACHE":       "/cache/gomod",
			"GOCACHE":          "/cache/gocache",
			"npm_config_cache": "/cache/npm",
		},
		Volumes:   append([]string{src + ":/src", out + ":/out"}, c.Mounts()...),
		DependsOn: map[string]Condition{CacheInitService: {Condition: "service_completed_successfully"}},
	}
}

// CacheInit is the CacheInitService service for a workload run by uid and
// gid. The caller adds the pull policy and limits.
func CacheInit(image string, uid, gid int, c Caches) Service {
	return Service{
		Image:       image,
		User:        "0:0",
		ReadOnly:    true,
		CapDrop:     []string{CapDropAll},
		CapAdd:      []string{"CHOWN"},
		SecurityOpt: []string{NoNewPrivileges},
		NetworkMode: "none",
		Volumes:     c.Mounts(),
		Command:     []string{"chown", User(uid, gid), "/cache/gomod", "/cache/gocache", "/cache/npm"},
	}
}

// EscapeInterpolation doubles every $ in what a container runs or reads, so
// compose passes it to the container as written instead of filling it from
// the environment compose itself runs in.
func EscapeInterpolation(s Service) Service {
	esc := func(v string) string { return strings.ReplaceAll(v, "$", "$$") }
	cmd := make([]string, len(s.Command))
	for i, c := range s.Command {
		cmd[i] = esc(c)
	}
	if s.Command != nil {
		s.Command = cmd
	}
	if s.Environment != nil {
		env := make(map[string]string, len(s.Environment))
		for k, v := range s.Environment {
			env[k] = esc(v)
		}
		s.Environment = env
	}
	if s.Healthcheck != nil {
		hc := *s.Healthcheck
		hc.Test = make([]string, len(s.Healthcheck.Test))
		for i, c := range s.Healthcheck.Test {
			hc.Test[i] = esc(c)
		}
		s.Healthcheck = &hc
	}
	return s
}
