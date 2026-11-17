package sandbox

import "net/netip"

// Network is a compose network definition.
type Network struct {
	Internal   bool              `yaml:"internal"`
	EnableIPv6 bool              `yaml:"enable_ipv6"`
	DriverOpts map[string]string `yaml:"driver_opts,omitempty"`
	IPAM       IPAM              `yaml:"ipam"`
}

// IPAM is a network's address configuration.
type IPAM struct {
	Config []Subnet `yaml:"config"`
}

// Subnet is one IPAM subnet.
type Subnet struct {
	Subnet string `yaml:"subnet"`
}

// DisableIPv6 turns IPv6 off inside a container on top of the networks
// having none, so no link-local address or route exists either.
var DisableIPv6 = map[string]string{
	"net.ipv6.conf.all.disable_ipv6":     "1",
	"net.ipv6.conf.default.disable_ipv6": "1",
}

// NetworkOf is an IPv4-only network on subnet. An internal network has no
// route out. A non-internal one has masquerade off, so it carries only
// published ports and its members cannot reach the internet through it.
func NetworkOf(subnet netip.Prefix, internal bool) Network {
	n := Network{
		Internal:   internal,
		EnableIPv6: false,
		IPAM:       IPAM{Config: []Subnet{{Subnet: subnet.Masked().String()}}},
	}
	if !internal {
		n.DriverOpts = map[string]string{"com.docker.network.bridge.enable_ip_masquerade": "false"}
	}
	return n
}
