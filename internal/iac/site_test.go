package iac

import (
	"strings"
	"testing"

	"github.com/dev-toolings/pvecli/internal/pve"
)

func TestGatewayFromNetworkPrefersTheBridge(t *testing.T) {
	// A node carries a gateway on several stanzas; the guests hang off the
	// bridge, so that is the one that answers for them.
	ifaces := []pve.NetIface{
		{Iface: "nic0", Type: "eth", Active: 1, Gateway: "192.0.2.1"},
		{Iface: "vmbr0", Type: "bridge", Active: 1, Gateway: "192.0.2.254"},
		{Iface: "wlp6s0", Type: "eth"},
	}
	gw, iface := GatewayFromNetwork(ifaces)
	if gw != "192.0.2.254" || iface != "vmbr0" {
		t.Errorf("GatewayFromNetwork = %q sur %q, attendu 192.0.2.254 sur vmbr0", gw, iface)
	}
}

func TestGatewayFromNetworkEmptyWhenNoneDeclared(t *testing.T) {
	gw, iface := GatewayFromNetwork([]pve.NetIface{{Iface: "vmbr0", Type: "bridge", Active: 1}})
	if gw != "" || iface != "" {
		t.Errorf("aucune passerelle déclarée doit rendre vide, pas %q/%q", gw, iface)
	}
}

func TestRenderTerraformSubstitutesTheSite(t *testing.T) {
	body := []byte("variable \"vm_ipv4_gateway\" {\n  default = null " + MarkerGateway +
		"\n}\n\nvariable \"vm_dns_servers\" {\n  default = null " + MarkerDNS + "\n}\n")

	got := string(RenderTerraform(body, Site{
		IPv4Gateway: "192.0.2.254",
		DNSServers:  []string{"192.0.2.254", "192.0.2.53"},
	}))

	for _, want := range []string{
		`  default = "192.0.2.254" ` + MarkerGateway,
		`  default = ["192.0.2.254", "192.0.2.53"] ` + MarkerDNS,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendu sans %q :\n%s", want, got)
		}
	}

	// The marker survives, otherwise a second scaffold would append a second
	// default instead of rewriting the first.
	if strings.Count(got, "default =") != 2 {
		t.Errorf("le rendu doit garder exactement deux défauts :\n%s", got)
	}
}

// Nothing known about the site renders null, and never a literal: a wrong
// gateway routes guests to an address nobody answers on, and says nothing.
func TestRenderTerraformUnknownSiteStaysNull(t *testing.T) {
	body := []byte("  default = \"192.0.2.1\" " + MarkerGateway + "\n")
	got := string(RenderTerraform(body, Site{}))
	if got != "  default = null "+MarkerGateway+"\n" {
		t.Errorf("site inconnu doit rendre null, pas %q", got)
	}
}
