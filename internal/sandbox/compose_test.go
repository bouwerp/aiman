package sandbox

import (
	"net/netip"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestComposeMemory(t *testing.T) {
	for _, tc := range []struct {
		mem  Memory
		want string
	}{
		{MemorySize("2g"), "mem_limit: 2g\n"},
		{MemoryBytes(512 << 20), "mem_limit: 536870912\n"},
	} {
		raw, err := yaml.Marshal(struct {
			M Memory `yaml:"mem_limit"`
		}{tc.mem})
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != tc.want {
			t.Fatalf("marshal %+v = %q, want %q", tc.mem, raw, tc.want)
		}
		var back struct {
			M Memory `yaml:"mem_limit"`
		}
		if err := yaml.Unmarshal(raw, &back); err != nil {
			t.Fatal(err)
		}
		if back.M != tc.mem {
			t.Fatalf("round trip %+v, want %+v", back.M, tc.mem)
		}
	}
}

func TestComposeWorkloadHardening(t *testing.T) {
	c := Caches{GoMod: "a", GoCache: "b", NPM: "c"}
	for name, s := range map[string]Service{
		"workload":   Workload("img", 1004, 1005, "/s", "/o", c),
		"cache-init": CacheInit("img", 1004, 1005, c),
	} {
		if !s.ReadOnly || !slices.Equal(s.CapDrop, []string{CapDropAll}) || !slices.Equal(s.SecurityOpt, []string{NoNewPrivileges}) {
			t.Fatalf("%s hardening = %+v", name, s)
		}
		if !slices.Contains(s.Volumes, "a:/cache/gomod") || slices.ContainsFunc(s.Volumes, func(v string) bool { return strings.HasSuffix(v, ":/cache/npm") && v != "c:/cache/npm" }) {
			t.Fatalf("%s volumes = %v", name, s.Volumes)
		}
	}
	w := Workload("img", 1004, 1005, "/s", "/o", c)
	if w.User != "1004:1005" || w.DependsOn[CacheInitService].Condition != "service_completed_successfully" {
		t.Fatalf("workload = %+v", w)
	}
	ci := CacheInit("img", 1004, 1005, c)
	if ci.User != "0:0" || ci.NetworkMode != "none" || !slices.Equal(ci.CapAdd, []string{"CHOWN"}) || slices.Contains(ci.Volumes, "/s:/src") {
		t.Fatalf("cache-init = %+v", ci)
	}
}

func TestComposeEscapeInterpolation(t *testing.T) {
	in := Service{
		Command:     []string{"sh", "-c", "echo $HOME"},
		Environment: map[string]string{"A": "${B}"},
		Healthcheck: &Healthcheck{Test: []string{"CMD-SHELL", "test $X"}},
	}
	out := EscapeInterpolation(in)
	if out.Command[2] != "echo $$HOME" || out.Environment["A"] != "$${B}" || out.Healthcheck.Test[1] != "test $$X" {
		t.Fatalf("escaped = %+v %+v", out, out.Healthcheck)
	}
	if in.Command[2] != "echo $HOME" || in.Environment["A"] != "${B}" || in.Healthcheck.Test[1] != "test $X" {
		t.Fatal("EscapeInterpolation changed its input")
	}
	if EscapeInterpolation(Service{}).Command != nil {
		t.Fatal("a nil command must stay nil")
	}
}

func TestComposeNetworkOf(t *testing.T) {
	p := netip.MustParsePrefix("10.231.1.7/24")
	in := NetworkOf(p, true)
	if !in.Internal || in.EnableIPv6 || in.DriverOpts != nil || in.IPAM.Config[0].Subnet != "10.231.1.0/24" {
		t.Fatalf("internal = %+v", in)
	}
	out := NetworkOf(p, false)
	if out.Internal || out.DriverOpts["com.docker.network.bridge.enable_ip_masquerade"] != "false" {
		t.Fatalf("outer = %+v", out)
	}
}
