package iac

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/dev-toolings/pvecli/internal/pve"
)

// Site carries the two network values the Terraform module cannot invent: the
// IPv4 gateway the guests must route through, and the resolvers they must ask.
//
// Neither has any business being a literal in the embedded module. A gateway
// written in the catalogue is right on the lab it was written on and silently
// wrong everywhere else — a guest then boots, gets an address, answers ping on
// the LAN, and has no egress at all. The node already knows the answer: it is
// the `gateway` of its own bridge, in GET /nodes/{node}/network.
type Site struct {
	// IPv4Gateway is empty when nothing could tell us. The module then renders
	// a null default, and Terraform fails loudly rather than routing guests to
	// an address nobody answers on.
	IPv4Gateway string

	// DNSServers is empty when the site does not override them; the module
	// then falls back to the gateway, which is what a box does.
	DNSServers []string

	// Source names where IPv4Gateway came from, for the line scaffold prints.
	Source string
}

// GatewayFromNetwork reads the site's IPv4 gateway off a node's own interfaces.
//
// A node has exactly one default gateway, but several interfaces may carry a
// `gateway` field in interfaces(5). The bridge the guests are attached to is
// the one that matters, so bridges win over physical devices, and an active
// interface wins over one that is only configured. The interface name is
// returned with it: scaffold says which one it believed, because "192.168.1.254
// read on vmbr0" is checkable and "192.168.1.254" is not.
func GatewayFromNetwork(ifaces []pve.NetIface) (gateway, iface string) {
	best := -1
	for _, n := range ifaces {
		if strings.TrimSpace(n.Gateway) == "" {
			continue
		}
		score := 0
		if n.Type == "bridge" {
			score += 2
		}
		if n.Active == 1 {
			score++
		}
		if score > best {
			best, gateway, iface = score, n.Gateway, n.Iface
		}
	}
	return gateway, iface
}

// Terraform markers. A line ending with one of these comments is rewritten by
// RenderTerraform when `pvecli iac scaffold` lays the module down. The marker
// stays in the written file: it is what lets a re-scaffold rewrite the value
// instead of appending a second default.
const (
	MarkerGateway = "# pvecli:ipv4_gateway"
	MarkerDNS     = "# pvecli:dns_servers"
)

var markerLine = regexp.MustCompile(`(?m)^([ \t]*).*(` +
	regexp.QuoteMeta(MarkerGateway) + `|` + regexp.QuoteMeta(MarkerDNS) + `)[ \t]*$`)

// RenderTerraform substitutes the site values into an embedded .tf asset.
//
// Rendering happens before the "modified locally?" comparison, so the file on
// disk is compared with what would be written for THIS site — otherwise every
// scaffold on a lab whose gateway is not the catalogue's would report the
// module as hand-edited.
func RenderTerraform(body []byte, site Site) []byte {
	return markerLine.ReplaceAllFunc(body, func(line []byte) []byte {
		indent := markerLine.FindSubmatch(line)[1]
		marker := string(markerLine.FindSubmatch(line)[2])

		value := "null"
		if marker == MarkerGateway {
			if site.IPv4Gateway != "" {
				value = fmt.Sprintf("%q", site.IPv4Gateway)
			}
		} else if len(site.DNSServers) > 0 {
			quoted := make([]string, 0, len(site.DNSServers))
			for _, s := range site.DNSServers {
				quoted = append(quoted, fmt.Sprintf("%q", s))
			}
			value = "[" + strings.Join(quoted, ", ") + "]"
		}
		return []byte(fmt.Sprintf("%sdefault = %s %s", indent, value, marker))
	})
}
