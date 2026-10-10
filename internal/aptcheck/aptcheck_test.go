package aptcheck

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/dev-toolings/pvecli/internal/pve"
)

func load[T any](t *testing.T, name string) T {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var env struct{ Data T }
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("%s : %v", name, err)
	}
	return env.Data
}

// Le nœud du lab, capturé le 10-10-2026 : déjà sur no-subscription, enterprise
// désactivé. La bascule n'a rien à faire, et doit le savoir.
func TestPlanNoSubscriptionIsEmptyOnASwitchedNode(t *testing.T) {
	repos := load[pve.AptRepositories](t, "apt-repositories.json")
	if plan := PlanNoSubscription(&repos); !plan.Empty() {
		t.Errorf("plan = %+v, want vide : le nœud est déjà sans enterprise", plan)
	}
}

// Un nœud tel qu'installé : pve-enterprise et ceph-squid enterprise actifs.
// Le plan ajoute les deux dépôts gratuits DE MÊME VERSION, avant de désactiver
// les deux stanzas enterprise.
func TestPlanNoSubscriptionOnAFreshInstall(t *testing.T) {
	repos := load[pve.AptRepositories](t, "apt-repositories-enterprise.json")
	plan := PlanNoSubscription(&repos)

	if len(plan.Add) != 2 || plan.Add[0] != "no-subscription" || plan.Add[1] != "ceph-squid-no-subscription" {
		t.Errorf("Add = %v, want [no-subscription ceph-squid-no-subscription]", plan.Add)
	}
	for _, h := range plan.Add {
		if h == "ceph-tentacle-no-subscription" || h == "test" {
			t.Errorf("Add contient %q : changer de série Ceph ou passer en test n'est pas une bascule de dépôt", h)
		}
	}
	if len(plan.Disable) != 2 {
		t.Fatalf("Disable = %+v, want les deux stanzas enterprise", plan.Disable)
	}
	for _, r := range plan.Disable {
		if r.Index != 0 || (r.Path != "/etc/apt/sources.list.d/ceph.sources" && r.Path != "/etc/apt/sources.list.d/pve-enterprise.sources") {
			t.Errorf("stanza inattendue : %+v", r)
		}
	}
}

func TestAssessOnTheLabNode(t *testing.T) {
	versions := load[[]pve.AptPackage](t, "apt-versions.json")
	updates := load[[]pve.AptPackage](t, "apt-update.json")
	rep := Assess("pve", versions, updates, true)

	if len(rep.Pending) != 139 || rep.Partial {
		t.Errorf("%d en attente (partiel=%v), want 139 complets", len(rep.Pending), rep.Partial)
	}
	if rep.Manager.Installed != "9.2.6" || rep.Manager.Candidate != "9.2.21" {
		t.Errorf("pve-manager = %+v", rep.Manager)
	}
	if !rep.KernelUpdate {
		t.Error("proxmox-kernel-7.0 passe de 7.0.14-8 à 7.0.14-23 : la mise à jour apporte un noyau")
	}
	// 7.0.14-8 tourne et c'est le plus récent installé (7.0.2-6 est plus
	// VIEUX, alors qu'une comparaison de chaînes le croirait plus récent).
	if rep.RunningKernel != "7.0.14-8-pve" || rep.NewestKernel != "7.0.14-8-pve" || rep.RebootRequired() {
		t.Errorf("noyau en cours %q, plus récent %q, reboot=%v", rep.RunningKernel, rep.NewestKernel, rep.RebootRequired())
	}
	if rep.RestartDaemons {
		t.Error("ManagerVersion = version installée : les démons sont à jour")
	}
	if !rep.Pending[0].Important {
		t.Errorf("les paquets suivis par /apt/versions doivent venir en tête, pas %+v", rep.Pending[0])
	}
	// Le lab pointe sur ceph-tentacle avec Ceph 19 installé : la mise à jour
	// est une montée majeure, et le rapport doit la voir.
	majors := rep.Majors()
	if len(majors) == 0 {
		t.Fatal("ceph 19 → 20 n'est pas vu comme une montée majeure")
	}
	for _, m := range majors {
		if m.Installed[:3] != "19." {
			t.Errorf("faux positif de montée majeure : %+v", m)
		}
	}
}

// Sans Sys.Modify, /apt/update répond 403 : le rapport se replie sur
// /apt/versions et le dit, au lieu d'annoncer « à jour ».
func TestAssessWithoutTheFullListIsPartial(t *testing.T) {
	versions := load[[]pve.AptPackage](t, "apt-versions.json")
	rep := Assess("pve", versions, nil, false)

	if !rep.Partial || !rep.UpdatesAvailable() {
		t.Fatalf("partiel=%v, en attente=%d", rep.Partial, len(rep.Pending))
	}
	for _, p := range rep.Pending {
		if p.Package == "proxmox-ve" {
			t.Error("proxmox-ve a la même version installée et candidate : rien à mettre à jour")
		}
	}
}

// Une liste complète VIDE veut dire « rien à faire », pas « pas le droit ».
func TestAssessEmptyFullListIsUpToDate(t *testing.T) {
	versions := []pve.AptPackage{{Package: "pve-manager", OldVersion: "9.2.21", Version: "9.2.21", ManagerVersion: "9.2.21/x"}}
	rep := Assess("pve", versions, []pve.AptPackage{}, true)
	if rep.Partial || rep.UpdatesAvailable() || rep.Verdict() != "à jour" {
		t.Errorf("rapport = %+v, verdict %q", rep, rep.Verdict())
	}
}

func TestRebootRequiredWhenANewerKernelIsInstalled(t *testing.T) {
	versions := []pve.AptPackage{
		{Package: "proxmox-ve", OldVersion: "9.2.0", Version: "9.2.0", RunningKernel: "7.0.14-8-pve"},
		{Package: "proxmox-kernel-7.0.14-8-pve-signed", OldVersion: "7.0.14-8", Version: "7.0.14-8", CurrentState: "Installed"},
		{Package: "proxmox-kernel-7.0.14-23-pve-signed", OldVersion: "7.0.14-23", Version: "7.0.14-23", CurrentState: "Installed"},
		{Package: "pve-manager", OldVersion: "9.2.21", Version: "9.2.21", ManagerVersion: "9.2.6/x"},
	}
	rep := Assess("pve", versions, []pve.AptPackage{}, true)
	if !rep.RebootRequired() || rep.NewestKernel != "7.0.14-23-pve" {
		t.Errorf("reboot=%v, plus récent %q", rep.RebootRequired(), rep.NewestKernel)
	}
	if !rep.RestartDaemons {
		t.Error("pve-manager 9.2.21 installé, démons sur 9.2.6 : il faut les redémarrer")
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"7.0.14-8-pve", "7.0.2-6-pve", 1},
		{"7.0.14-23-pve", "7.0.14-8-pve", 1},
		{"9.2.6", "9.2.21", -1},
		{"9.2.6", "9.2.6", 0},
		{"20.2.4-pve5", "19.2.3-pve4", 1},
	} {
		if got := CompareVersions(c.a, c.b); sign(got) != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}
