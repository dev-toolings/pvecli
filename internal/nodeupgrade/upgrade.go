// Package nodeupgrade is the on-node half of « pvecli node upgrade ».
//
// The Proxmox API can refresh the package index (POST /apt/update is
// apt-get update) and can reboot the node. It cannot install packages.
// The script below is what actually runs, over the operator's ssh, the same
// way « node nag » reaches a file the API does not expose.
package nodeupgrade

import (
	"sort"
	"strconv"
)

// Script is the exact text sent to `sh -s` on the node.
//
// apt-get, not apt: the apt(8) command line is not a stable interface for
// scripts. full-upgrade, never upgrade: on Proxmox, upgrade is allowed to
// hold a kernel back. The two dpkg options keep a conffile the operator
// already changed, and answer the question without a prompt. needrestart is
// suspended so it cannot restart qemu or pveproxy in the middle of the
// transaction; a new kernel still needs the separate reboot.
func Script() string {
	return "" +
		"set -eu\n" +
		"export DEBIAN_FRONTEND=noninteractive\n" +
		"export APT_LISTCHANGES_FRONTEND=none\n" +
		"export NEEDRESTART_SUSPEND=1\n" +
		"apt-get update\n" +
		"apt-get -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold full-upgrade\n"
}

// Guest is what the reboot decision needs to know about one VM or container.
//
// OnBoot is the configured flag, not the current status. A guest that is
// running with OnBoot false will not come back. A guest that is stopped with
// OnBoot true will start. Templates are excluded from both lists: they are
// not services.
type Guest struct {
	VMID     int
	Name     string
	Status   string
	OnBoot   bool
	Template bool
}

// Label is how the guest appears in the report: « 235 ploydok-staging-01 ».
func (g Guest) Label() string {
	if g.Name == "" {
		return strconv.Itoa(g.VMID)
	}
	return strconv.Itoa(g.VMID) + " " + g.Name
}

// Boot is the split an operator has to read before a node reboot.
type Boot struct {
	// StayDown: running, not a template, onboot is not 1.
	StayDown []Guest
	// WillStart: not running, not a template, onboot is 1.
	WillStart []Guest
}

// Classify splits guests into the two groups a reboot would surprise.
func Classify(guests []Guest) Boot {
	var boot Boot
	for _, g := range guests {
		if g.Template {
			continue
		}
		switch {
		case g.Status == "running" && !g.OnBoot:
			boot.StayDown = append(boot.StayDown, g)
		case g.Status != "running" && g.OnBoot:
			boot.WillStart = append(boot.WillStart, g)
		}
	}
	sort.Slice(boot.StayDown, func(i, j int) bool { return boot.StayDown[i].VMID < boot.StayDown[j].VMID })
	sort.Slice(boot.WillStart, func(i, j int) bool { return boot.WillStart[i].VMID < boot.WillStart[j].VMID })
	return boot
}

// Labels joins guest labels in VMID order. An empty group is « aucun »,
// so a report that looked and found nothing does not look like a report
// that forgot to look.
func Labels(guests []Guest) string {
	if len(guests) == 0 {
		return "aucun"
	}
	out := guests[0].Label()
	for _, g := range guests[1:] {
		out += ", " + g.Label()
	}
	return out
}

// NextStep is the one line that says what remains after apt has finished.
//
// requested is --reboot. A reboot is performed only when a newer kernel than
// the one running is already on disk. Passing --reboot for a curl update
// does not power-cycle the hypervisor.
func NextStep(node string, rebootRequired, requested bool) string {
	switch {
	case !rebootRequired && requested:
		return "pas de nouveau noyau, pas de redémarrage"
	case !rebootRequired:
		return "rien : le noyau en cours est le plus récent"
	case requested:
		return "redémarrage de " + node
	default:
		return "pvecli node reboot " + node
	}
}
