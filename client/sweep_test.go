package client

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	ip, n, err := net.ParseCIDR(s)
	require.NoError(t, err)
	// ParseCIDR masks the address off into n.IP; the sweep needs the host's own
	// address there, which is what net.Interface.Addrs reports.
	n.IP = ip
	return n
}

func TestSubnetHostsSkipsNetworkBroadcastAndSelf(t *testing.T) {
	hosts := subnetHosts(mustCIDR(t, "192.168.1.100/24"))

	// 254 usable addresses, less our own.
	assert.Len(t, hosts, 253)

	got := map[string]bool{}
	for _, ip := range hosts {
		got[ip.String()] = true
	}
	assert.NotContains(t, got, "192.168.1.0", "network address")
	assert.NotContains(t, got, "192.168.1.255", "broadcast address")
	assert.NotContains(t, got, "192.168.1.100", "the sweeping host itself")
	assert.Contains(t, got, "192.168.1.1")
	assert.Contains(t, got, "192.168.1.254")
}

// Nearest first is what lets the sweep answer quickly on a home network, where
// the device is usually a few addresses away.
func TestSubnetHostsOrdersOutwardsFromSelf(t *testing.T) {
	hosts := subnetHosts(mustCIDR(t, "192.168.1.100/24"))
	require.GreaterOrEqual(t, len(hosts), 4)

	assert.Equal(t, "192.168.1.101", hosts[0].String())
	assert.Equal(t, "192.168.1.99", hosts[1].String())
	assert.Equal(t, "192.168.1.102", hosts[2].String())
	assert.Equal(t, "192.168.1.98", hosts[3].String())
}

// A /22 is the case the design has to handle: a /24 assumption breaks
// discovery on such networks.
func TestSubnetHostsHandlesSlash22(t *testing.T) {
	hosts := subnetHosts(mustCIDR(t, "192.168.68.63/22"))
	assert.Len(t, hosts, 1021) // 1022 usable, less our own

	got := map[string]bool{}
	for _, ip := range hosts {
		got[ip.String()] = true
	}
	assert.Contains(t, got, "192.168.68.62", "the address the unit moved to")
	assert.Contains(t, got, "192.168.71.254")
	assert.NotContains(t, got, "192.168.71.255", "broadcast address of a /22")
	assert.NotContains(t, got, "192.168.68.0", "network address of a /22")
}

// Walking outwards must not underflow past the network address when the host sits
// at the bottom of the range.
func TestSubnetHostsAtBottomOfRange(t *testing.T) {
	hosts := subnetHosts(mustCIDR(t, "10.0.0.1/24"))
	assert.Len(t, hosts, 253)

	for _, ip := range hosts {
		assert.NotEqual(t, "10.0.0.0", ip.String())
		assert.NotEqual(t, "10.0.0.1", ip.String())
		assert.NotEqual(t, "10.0.0.255", ip.String())
	}
}

func TestSubnetHostsAtTopOfRange(t *testing.T) {
	hosts := subnetHosts(mustCIDR(t, "10.0.0.254/24"))
	assert.Len(t, hosts, 253)
	assert.Equal(t, "10.0.0.253", hosts[0].String(), "nothing above us to try first")
}

func TestSubnetHostsRefusesUnusableNetworks(t *testing.T) {
	for _, cidr := range []string{
		"10.0.0.1/16", // 65534 hosts, far past MaxSweepHosts
		"10.0.0.1/8",
		"10.0.0.1/19", // 8190 hosts, just past MaxSweepHosts
		"10.0.0.1/31", // no other host
		"10.0.0.1/32",
	} {
		assert.Nil(t, subnetHosts(mustCIDR(t, cidr)), cidr)
	}
}

// A /20 is the widest network the sweep will accept.
func TestSubnetHostsAcceptsTheWidestAllowedNetwork(t *testing.T) {
	hosts := subnetHosts(mustCIDR(t, "10.0.0.1/20"))
	assert.Len(t, hosts, MaxSweepHosts-1)
}

// Some VPN adapters report an all-zero mask, whose network is the whole internet.
func TestSubnetHostsRefusesZeroMask(t *testing.T) {
	assert.Nil(t, subnetHosts(&net.IPNet{
		IP:   net.IPv4(10, 0, 0, 1),
		Mask: net.IPv4Mask(0, 0, 0, 0),
	}))
}

func TestSubnetHostsIgnoresIPv6(t *testing.T) {
	_, n, err := net.ParseCIDR("fe80::1/64")
	require.NoError(t, err)
	assert.Nil(t, subnetHosts(n))
}

func TestIPUintRoundTrip(t *testing.T) {
	for _, s := range []string{"0.0.0.0", "10.0.0.1", "192.168.71.255", "255.255.255.255"} {
		assert.Equal(t, s, uint32ToIP4(ip4ToUint(net.ParseIP(s))).String())
	}
}
