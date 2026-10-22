package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/dev-toolings/pvecli/internal/nodeupgrade"
	"github.com/dev-toolings/pvecli/internal/pve"
	"github.com/dev-toolings/pvecli/internal/testutil"
)

func TestNodeUpgradeDryRunPrintsTheScriptAndNothingElse(t *testing.T) {
	stdout, stderr, err := run(t, "node", "upgrade", "--ssh-host", "192.0.2.23", "--dry-run")
	if err != nil {
		t.Fatalf("--dry-run : %v", err)
	}
	if stdout != nodeupgrade.Script() {
		t.Errorf("stdout n'est pas le script qui serait envoyé :\n%s", stdout)
	}
	if !strings.Contains(stderr, "root@192.0.2.23") {
		t.Errorf("stderr doit nommer la cible SSH :\n%s", stderr)
	}
	if strings.Contains(stdout, "apt-get upgrade") || strings.Contains(stdout, "apt upgrade") {
		t.Errorf("le script lance « upgrade » sans full- :\n%s", stdout)
	}
}

func TestNodeUpgradeRefusesWithoutATerminal(t *testing.T) {
	srv := testutil.New(t, "../testdata", map[string]string{})
	point(t, srv.URL)

	_, _, err := run(t, "node", "upgrade", "pve", "--ssh-host", "192.0.2.23")
	if err == nil {
		t.Fatal("sans terminal et sans --yes, la commande doit refuser avant d'ouvrir SSH")
	}
	var coded interface{ ExitCode() int }
	if !errors.As(err, &coded) || coded.ExitCode() != pve.ExitConfirm {
		t.Fatalf("code = %v, want %d (%v)", err, pve.ExitConfirm, err)
	}
	if len(srv.Requests) != 0 {
		t.Fatalf("une requête est partie avant la confirmation : %v", srv.Requests)
	}
}

func TestNodeUpgradeHelpIsNotTheBinaryUpgrade(t *testing.T) {
	stdout, _, err := run(t, "node", "upgrade", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"full-upgrade", "onboot=1", "pvecli upgrade", "node reboot"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("l'aide ne contient pas %q :\n%s", want, stdout)
		}
	}
}

// --reboot lit les invités AVANT apt. Une liste illisible doit s'arrêter là :
// installer puis découvrir qu'on ne sait pas qui reviendrait est le scénario
// que l'ordre des étapes existe pour empêcher.
func TestNodeUpgradeRebootStopsBeforeAptWhenGuestsAreUnreadable(t *testing.T) {
	srv := testutil.New(t, "../testdata", map[string]string{})
	point(t, srv.URL)

	_, _, err := run(t, "node", "upgrade", "pve", "--ssh-host", "192.0.2.23", "--yes", "--reboot")
	if err == nil {
		t.Fatal("--reboot sans liste d'invités doit échouer")
	}
	if !strings.Contains(err.Error(), "Rien n'a été installé") {
		t.Errorf("l'erreur doit dire que rien n'a été installé : %v", err)
	}
	if len(srv.Requests) == 0 {
		t.Fatal("aucune lecture API : la commande est partie vers SSH trop tôt, ou pas du tout")
	}
	for _, req := range srv.Requests {
		if req != "GET /api2/json/nodes/pve/qemu" && req != "GET /api2/json/nodes/pve/lxc" {
			t.Errorf("requête inattendue avant l'échec : %s", req)
		}
	}
}
