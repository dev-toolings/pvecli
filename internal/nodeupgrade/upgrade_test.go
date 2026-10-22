package nodeupgrade

import (
	"strings"
	"testing"
)

func TestScriptIsFullUpgradeAndNeverUpgrade(t *testing.T) {
	script := Script()
	for _, want := range []string{
		"DEBIAN_FRONTEND=noninteractive",
		"APT_LISTCHANGES_FRONTEND=none",
		"NEEDRESTART_SUSPEND=1",
		"apt-get update",
		"Dpkg::Options::=--force-confdef",
		"Dpkg::Options::=--force-confold",
		"full-upgrade",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script sans %q:\n%s", want, script)
		}
	}
	// « apt-get full-upgrade » contains the letters of « upgrade » but must
	// not contain the command upgrade itself. A line that says upgrade
	// without full-upgrade is the Proxmox footgun.
	for _, line := range strings.Split(script, "\n") {
		if strings.Contains(line, "upgrade") && !strings.Contains(line, "full-upgrade") {
			t.Errorf("ligne qui lance « upgrade » sans « full- » : %q", line)
		}
	}
	if !strings.HasSuffix(script, "\n") {
		t.Error("le script doit finir par un saut de ligne : il est l'entrée de sh -s")
	}
}

func TestClassifySeparatesWhoComesBack(t *testing.T) {
	boot := Classify([]Guest{
		{VMID: 240, Name: "hermes-lab-01", Status: "running", OnBoot: false},
		{VMID: 214, Name: "scrap-agent-01", Status: "running", OnBoot: true},
		{VMID: 235, Name: "ploydok-staging-01", Status: "running", OnBoot: false},
		{VMID: 225, Name: "ohbot-preprod-01", Status: "stopped", OnBoot: true},
		{VMID: 9001, Name: "debian13-cloudinit-agent", Status: "stopped", Template: true},
		{VMID: 230, Name: "tools", Status: "stopped", OnBoot: false},
	})

	if got := Labels(boot.StayDown); got != "235 ploydok-staging-01, 240 hermes-lab-01" {
		t.Errorf("StayDown = %q", got)
	}
	if got := Labels(boot.WillStart); got != "225 ohbot-preprod-01" {
		t.Errorf("WillStart = %q", got)
	}
}

func TestLabelsOfNothingSaysAucun(t *testing.T) {
	if got := Labels(nil); got != "aucun" {
		t.Errorf("Labels(nil) = %q", got)
	}
	if got := (Guest{VMID: 235}).Label(); got != "235" {
		t.Errorf("label sans nom = %q", got)
	}
}

func TestNextStep(t *testing.T) {
	cases := []struct {
		required, requested bool
		want                string
	}{
		{false, false, "rien : le noyau en cours est le plus récent"},
		{false, true, "pas de nouveau noyau, pas de redémarrage"},
		{true, false, "pvecli node reboot pve"},
		{true, true, "redémarrage de pve"},
	}
	for _, tc := range cases {
		if got := NextStep("pve", tc.required, tc.requested); got != tc.want {
			t.Errorf("NextStep(%v, %v) = %q, want %q", tc.required, tc.requested, got, tc.want)
		}
	}
}
