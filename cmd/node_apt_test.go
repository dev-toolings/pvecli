package cmd

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/dev-toolings/pvecli/internal/testutil"
)

// Le plan de la bascule doit nommer chaque écriture ET la commande exacte qui
// la défait : c'est ce que l'opérateur lit avant de confirmer.
func TestNodeRepoNoSubPlanOnAFreshInstall(t *testing.T) {
	srv := testutil.New(t, "../testdata", map[string]string{
		"GET /api2/json/nodes/pve/apt/repositories": "apt-repositories-enterprise.json",
	})
	point(t, srv.URL)

	_, stderr, err := run(t, "node", "repo", "no-subscription", "pve", "--dry-run")
	if err != nil {
		t.Fatalf("--dry-run : %v", err)
	}
	for _, want := range []string{
		"handle=no-subscription", "handle=ceph-squid-no-subscription",
		"pve-enterprise.sources index=0 enabled=0",
		"pvesh create /nodes/pve/apt/repositories --path /etc/apt/sources.list.d/pve-enterprise.sources --index 0 --enabled 1",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("le plan ne contient pas %q :\n%s", want, stderr)
		}
	}
	for _, req := range srv.Requests {
		if !strings.HasPrefix(req, "GET ") {
			t.Errorf("--dry-run a émis %s", req)
		}
	}
}

func TestNodeRepoNoSubIsIdempotent(t *testing.T) {
	srv := testutil.New(t, "../testdata", map[string]string{
		"GET /api2/json/nodes/pve/apt/repositories": "apt-repositories.json",
	})
	point(t, srv.URL)

	_, stderr, err := run(t, "node", "repo", "no-subscription", "pve", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "rien à faire") || len(srv.Requests) != 1 {
		t.Errorf("requêtes %v, stderr :\n%s", srv.Requests, stderr)
	}
}

func TestNodeUpdatesExitCodeAndCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	srv := testutil.New(t, "../testdata", map[string]string{
		"GET /api2/json/nodes/pve/apt/versions": "apt-versions.json",
		"GET /api2/json/nodes/pve/apt/update":   "apt-update.json",
	})
	point(t, srv.URL)

	stdout, stderr, err := run(t, "node", "updates", "pve", "--exit-code")
	var coded interface{ ExitCode() int }
	if !errors.As(err, &coded) || coded.ExitCode() != exitUpdatesAvailable {
		t.Fatalf("err = %v, want code %d", err, exitUpdatesAvailable)
	}
	if !strings.Contains(stdout, "139 mise(s) à jour") || !strings.Contains(stdout, "9.2.6 → 9.2.21") {
		t.Errorf("stdout :\n%s", stdout)
	}
	if !strings.Contains(stderr, "pvecli node upgrade") {
		t.Errorf("le conseil doit pointer vers node upgrade :\n%s", stderr)
	}

	// La commande a rempli le cache : --notify le lit sans réseau.
	srv.Close()
	stdout, _, err = run(t, "node", "updates", "pve", "--notify")
	if err != nil || !strings.Contains(stdout, "nœud pve : 139 mise(s) à jour") {
		t.Errorf("--notify = %q, %v", stdout, err)
	}
}

// Sans Sys.Modify, /apt/update répond 403 : la commande répond quand même,
// sur les paquets Proxmox, et dit que c'est partiel.
func TestNodeUpdatesDegradesOnForbidden(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	srv := testutil.New(t, "../testdata", map[string]string{
		"GET /api2/json/nodes/pve/apt/versions": "apt-versions.json",
	})
	point(t, srv.URL)
	srv.Config.Handler = forbidPath(srv.Config.Handler, "/api2/json/nodes/pve/apt/update")

	stdout, stderr, err := run(t, "node", "updates", "pve")
	if err != nil {
		t.Fatalf("un 403 sur la liste complète ne doit pas faire échouer : %v", err)
	}
	if !strings.Contains(stdout, "(partiel)") || !strings.Contains(stderr, "Sys.Modify") {
		t.Errorf("stdout :\n%s\nstderr :\n%s", stdout, stderr)
	}
}

// forbidPath answers 403 on one path, the way a node does when the token
// lacks the privilege, and hands everything else to next.
func forbidPath(next http.Handler, path string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == path {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"data":null,"message":"Permission check failed (/nodes/pve, Sys.Modify)\n"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
