package cmd

import (
	"strings"
	"testing"

	"github.com/dev-toolings/pvecli/internal/testutil"
)

// memRoutes pairs two captures of the same guest: what PVE says about it, and
// what its own kernel says. The whole command exists because those two answers
// disagree, so a fixture pair taken from different machines would test nothing.
func memRoutes() map[string]string {
	return map[string]string{
		"GET /api2/json/nodes/pve/qemu/211/status/current":  "qemu-status-mem.json",
		"GET /api2/json/nodes/pve/qemu/211/agent/file-read": "agent-meminfo.json",
	}
}

// The point of the command: both readings are present, and the truthful one
// does not quietly replace the one the console keeps showing.
func TestVMMemShowsBothReadings(t *testing.T) {
	srv := testutil.New(t, "../testdata", memRoutes())
	point(t, srv.URL)

	stdout, _, err := run(t, "vm", "mem", "211", "--node", "pve")
	if err != nil {
		t.Fatalf("vm mem a échoué : %v", err)
	}

	for _, want := range []string{
		"7.1 GiB / 8.0 GiB", // la vue Proxmox, cache compris
		"1.6 GiB",           // réellement utilisé, total moins disponible
		"6.1 GiB",           // disponible, la seule réponse à « est-ce serré ? »
		"5.8 GiB",           // le cache récupérable qui explique tout l'écart
		"pression mémoire",  // le compteur qui tranche
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("sortie sans %q :\n%s", want, stdout)
		}
	}
}

// The status reading alone must never be presented as the answer: if the agent
// is unreachable, the command fails rather than falling back to the figure it
// exists to correct. The translation of that failure into a message naming the
// missing package is covered by TestAgentMeminfoTranslatesAMissingAgent, which
// can produce the 500 PVE actually returns.
func TestVMMemRefusesToFallBackOnTheMisleadingFigure(t *testing.T) {
	srv := testutil.New(t, "../testdata", map[string]string{
		"GET /api2/json/nodes/pve/qemu/211/status/current": "qemu-status-mem.json",
	})
	point(t, srv.URL)

	stdout, _, err := run(t, "vm", "mem", "211", "--node", "pve")
	if err == nil {
		t.Fatal("un agent muet doit faire échouer la commande")
	}
	if strings.Contains(stdout, "7.1 GiB") {
		t.Errorf("la vue Proxmox a été rendue malgré l'absence de la vérité invité :\n%s", stdout)
	}
}

// The JSON form must carry the guest's own figures, not only PVE's, otherwise
// anything scripted on top of it inherits the misleading reading.
func TestVMMemJSONCarriesTheGuestFigures(t *testing.T) {
	srv := testutil.New(t, "../testdata", memRoutes())
	point(t, srv.URL)

	stdout, _, err := run(t, "vm", "mem", "211", "--node", "pve", "--output", "json")
	if err != nil {
		t.Fatalf("vm mem --output json a échoué : %v", err)
	}
	for _, want := range []string{"available", "cache", "used"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("JSON sans le champ %q :\n%s", want, stdout)
		}
	}
}
