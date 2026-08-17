package cmd

import (
	"strings"
	"testing"

	"github.com/dev-toolings/pvecli/internal/testutil"
)

// Le ratio mémoire seul est le chiffre le plus trompeur de la CLI. PVE le
// calcule comme total_mem moins free_mem, relevé sur virtio-balloon : le cache
// de pages de l'invité y compte comme occupé, et une VM Docker en bonne santé
// affiche donc près de 100 %. La ligne doit porter de quoi lever le doute sans
// quitter la commande, sinon l'opérateur part chercher une fuite qui n'existe
// pas. Les trois chiffres viennent de la réponse que « show » lit déjà : la
// ligne ne coûte aucun appel de plus, ce que la liste des requêtes vérifie.
func TestLaLigneMemoireDesamorceLeRatioTrompeur(t *testing.T) {
	srv := testutil.New(t, "../testdata", map[string]string{
		"GET /api2/json/nodes/pve/qemu/211/config":         "qemu-config.json",
		"GET /api2/json/nodes/pve/qemu/211/status/current": "qemu-status.json",
	})
	point(t, srv.URL)

	stdout, _, err := run(t, "vm", "show", "211", "--node", "pve")
	if err != nil {
		t.Fatalf("vm show : %v", err)
	}

	for _, want := range []string{
		"390.6 MiB / 2.0 GiB", // le ratio d'origine, toujours là
		"libre invité 1.5 GiB",
		"hôte 536.3 MiB",
		"pression 0.00 %",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("la ligne mémoire ne contient pas %q :\n%s", want, stdout)
		}
	}

	// `full` à zéro est la lecture normale : la mentionner à chaque fois
	// noierait le seul cas où elle veut dire quelque chose.
	if strings.Contains(stdout, "bloqué") {
		t.Errorf("« bloqué » ne doit apparaître que si full dépasse zéro :\n%s", stdout)
	}

	if len(srv.Requests) != 2 {
		t.Errorf("la ligne mémoire doit tenir dans les appels existants, requêtes = %v", srv.Requests)
	}
}

// Un conteneur ne rapporte ni freemem, ni memhost : ces chiffres viennent de
// virtio-balloon et du process QEMU, dont un LXC n'a ni l'un ni l'autre. Zéro
// n'y est pas une mesure mais une absence, et la ligne doit retomber sur le
// ratio nu plutôt que d'annoncer « libre invité 0 B », qui se lirait comme une
// alerte. Le PSI, lui, existe bien côté conteneur et doit rester affiché.
func TestSurUnConteneurLaLigneMemoireNInventePasLesChiffresAbsents(t *testing.T) {
	srv := testutil.New(t, "../testdata", map[string]string{
		"GET /api2/json/nodes/pve/lxc/121/config":         "qemu-config.json",
		"GET /api2/json/nodes/pve/lxc/121/status/current": "lxc-status-running.json",
	})
	point(t, srv.URL)

	stdout, _, err := run(t, "lxc", "show", "121", "--node", "pve")
	if err != nil {
		t.Fatalf("lxc show : %v", err)
	}

	if !strings.Contains(stdout, "275.0 MiB / 4.0 GiB") {
		t.Errorf("le ratio doit rester affiché :\n%s", stdout)
	}
	for _, absent := range []string{"libre invité", "hôte "} {
		if strings.Contains(stdout, absent) {
			t.Errorf("%q n'existe pas sur un LXC et ne doit pas être inventé :\n%s", absent, stdout)
		}
	}
	// Zéro est la lecture normale d'un compteur PSI, et elle est informative :
	// elle dit « aucune pression », ce qui n'est pas « je n'en sais rien ».
	if !strings.Contains(stdout, "pression 0.00 %") {
		t.Errorf("une pression nulle est une mesure, pas une absence :\n%s", stdout)
	}
}
