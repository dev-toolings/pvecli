package pve

import (
	"context"
	"testing"
)

// PVE ne renvoie pas les compteurs PSI dans le même type JSON selon le genre
// d'invité : nombre sur QEMU, chaîne entre guillemets sur LXC. Même champ, même
// version du nœud, deux types. Un décodage strict en float64 passe donc sur les
// VM et casse sur les conteneurs, ce qui est le pire des deux mondes : la
// commande marche là où on la teste d'abord.
func TestLePSISeDecodeQuIlSoitNombreOuChaine(t *testing.T) {
	c := replay(t, map[string]string{
		"GET /api2/json/nodes/pve/qemu/211/status/current": "qemu-status.json",
		"GET /api2/json/nodes/pve/lxc/121/status/current":  "lxc-status-running.json",
	})

	vm, err := c.GuestStatus(context.Background(), "pve", TypeQEMU, 211)
	if err != nil {
		t.Fatalf("GuestStatus qemu : %v", err)
	}
	if vm.PressureMemorySome == nil {
		t.Fatal("le PSI d'une VM ne doit pas être nil : la réponse le porte")
	}
	if got := vm.PressureMemorySome.Float(); got != 0 {
		t.Errorf("PSI qemu = %v, want 0", got)
	}

	// Le cas qui cassait : "0.00" et non 0.
	ct, err := c.GuestStatus(context.Background(), "pve", TypeLXC, 121)
	if err != nil {
		t.Fatalf("GuestStatus lxc : %v", err)
	}
	if ct.PressureMemorySome == nil {
		t.Fatal("le PSI d'un conteneur ne doit pas être nil : la réponse le porte")
	}
	if got := ct.PressureMemorySome.Float(); got != 0 {
		t.Errorf("PSI lxc = %v, want 0", got)
	}
}

// Un conteneur ne rapporte ni freemem ni memhost : ces deux chiffres viennent
// de virtio-balloon et du process QEMU, dont un LXC n'a ni l'un ni l'autre.
// Zéro doit donc rester zéro, pour que l'affichage sache se taire plutôt que
// d'annoncer « libre invité 0 B », qui se lirait comme une alerte.
func TestUnConteneurNeRapportePasLaMemoireInvitee(t *testing.T) {
	c := replay(t, map[string]string{
		"GET /api2/json/nodes/pve/lxc/121/status/current": "lxc-status-running.json",
	})

	ct, err := c.GuestStatus(context.Background(), "pve", TypeLXC, 121)
	if err != nil {
		t.Fatalf("GuestStatus : %v", err)
	}
	if ct.FreeMem != 0 || ct.MemHost != 0 {
		t.Errorf("freemem = %d, memhost = %d, attendus absents sur un LXC", ct.FreeMem, ct.MemHost)
	}
}

// Sur une VM, les trois chiffres qui désamorcent le ratio doivent survivre au
// décodage. C'est la fixture réelle du lab : 390 Mio « utilisés » sur 2 Gio,
// dont 1.5 Gio de cache que le ratio compte comme occupés.
func TestUneVMRapporteLaMemoireInviteeEtHote(t *testing.T) {
	c := replay(t, map[string]string{
		"GET /api2/json/nodes/pve/qemu/211/status/current": "qemu-status.json",
	})

	vm, err := c.GuestStatus(context.Background(), "pve", TypeQEMU, 211)
	if err != nil {
		t.Fatalf("GuestStatus : %v", err)
	}
	if vm.FreeMem != 1660452864 {
		t.Errorf("freemem = %d, want 1660452864", vm.FreeMem)
	}
	if vm.MemHost != 562384896 {
		t.Errorf("memhost = %d, want 562384896", vm.MemHost)
	}
}
