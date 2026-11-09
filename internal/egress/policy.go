// Package egress is the single outbound HTTP client (CLAUDE.md rule 13, architecture §11).
// Every server-side request (git remotes, OIDC discovery, the plugin registry, approved
// embeds, webhooks) goes through a Client, which only connects to globally reachable unicast
// addresses unless the admin approved that exact host and port for that purpose.
package egress

import (
	"net/netip"
)

// blocked lists the IANA special-purpose blocks that aren't globally reachable, plus the
// IPv6 blocks that embed an IPv4 address (which could be a private one). Addresses are
// unmapped before the check, so IPv4-mapped IPv6 (::ffff:0:0/96) is covered by the IPv4
// entries.
var blocked = mustPrefixes(
	// IPv4
	"0.0.0.0/8",       // "this network"
	"10.0.0.0/8",      // private
	"100.64.0.0/10",   // carrier-grade NAT; also Tailscale addresses
	"127.0.0.0/8",     // loopback
	"169.254.0.0/16",  // link-local, including cloud metadata endpoints
	"172.16.0.0/12",   // private
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // documentation
	"192.88.99.0/24",  // deprecated 6to4 relay anycast
	"192.168.0.0/16",  // private
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // documentation
	"203.0.113.0/24",  // documentation
	"224.0.0.0/4",     // multicast
	"240.0.0.0/4",     // reserved, including broadcast
	// IPv6
	"::/96",          // unspecified, loopback and deprecated IPv4-compatible addresses
	"64:ff9b::/96",   // NAT64 (embeds IPv4)
	"64:ff9b:1::/48", // local-use NAT64 (embeds IPv4)
	"100::/64",       // discard
	"2001::/23",      // IETF protocol assignments, including Teredo (2001::/32, embeds IPv4)
	"2001:db8::/32",  // documentation
	"2002::/16",      // 6to4 (embeds IPv4)
	"3fff::/20",      // documentation
	"5f00::/16",      // segment routing SIDs
	"fc00::/7",       // unique local
	"fe80::/10",      // link-local
	"ff00::/8",       // multicast
)

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// Public reports whether a connection to addr is allowed without an exception: a globally
// reachable unicast address.
func Public(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || !addr.IsGlobalUnicast() {
		return false
	}
	for _, p := range blocked {
		if p.Contains(addr) {
			return false
		}
	}
	return true
}

// unicast reports whether addr may be reached through an approved exception: anything but
// unspecified, multicast or broadcast. An exception names one host for one purpose, which
// is what lets an admin reach, say, a Grafana on the LAN (architecture §11).
func unicast(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsValid() && !addr.IsUnspecified() && !addr.IsMulticast() &&
		addr != netip.MustParseAddr("255.255.255.255")
}
