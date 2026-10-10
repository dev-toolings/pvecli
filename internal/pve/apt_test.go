package pve

import (
	"context"
	"net/http"
	"testing"
)

func TestAptReadsDecodeRealAnswers(t *testing.T) {
	c := replay(t, map[string]string{
		"GET /api2/json/nodes/pve/apt/repositories": "apt-repositories.json",
		"GET /api2/json/nodes/pve/apt/update":       "apt-update.json",
		"GET /api2/json/nodes/pve/apt/versions":     "apt-versions.json",
	})
	ctx := context.Background()

	repos, err := c.AptRepositories(ctx, "pve")
	if err != nil {
		t.Fatalf("AptRepositories: %v", err)
	}
	if repos.Digest == "" || len(repos.Files) == 0 {
		t.Fatalf("dépôts = %+v", repos)
	}
	// « status » absent, 0 et 1 sont trois états : le décodage doit les
	// garder distincts.
	ent, _ := repos.Standard("enterprise")
	nosub, _ := repos.Standard("no-subscription")
	test, _ := repos.Standard("test")
	if ent.Status == nil || *ent.Status != 0 || !nosub.Enabled() || test.Status != nil {
		t.Errorf("enterprise=%v no-subscription=%v test=%v", ent.Status, nosub.Status, test.Status)
	}

	updates, err := c.AptUpdates(ctx, "pve")
	if err != nil || len(updates) != 139 {
		t.Fatalf("AptUpdates: %d paquets, %v", len(updates), err)
	}

	versions, err := c.AptVersions(ctx, "pve")
	if err != nil {
		t.Fatalf("AptVersions: %v", err)
	}
	for _, p := range versions {
		if p.Package == "proxmox-ve" && p.RunningKernel != "7.0.14-8-pve" {
			t.Errorf("RunningKernel = %q", p.RunningKernel)
		}
	}
}

func TestAptRepoIsEnterpriseLooksAtTheHost(t *testing.T) {
	for uri, want := range map[string]bool{
		"https://enterprise.proxmox.com/debian/pve":        true,
		"https://enterprise.proxmox.com/debian/ceph-squid": true,
		"http://download.proxmox.com/debian/pve":           false,
	} {
		if got := (AptRepository{URIs: []string{uri}}).IsEnterprise(); got != want {
			t.Errorf("%s : %v, want %v", uri, got, want)
		}
	}
}

func TestAptWritesSendWhatTheSchemaNames(t *testing.T) {
	ctx := context.Background()

	t.Run("add standard repo", func(t *testing.T) {
		c, s := newSpy(t, `{"data":null}`)
		if err := c.AddStandardRepo(ctx, "pve", "no-subscription", "abc"); err != nil {
			t.Fatal(err)
		}
		if s.method != http.MethodPut || s.path != "/api2/json/nodes/pve/apt/repositories" ||
			s.body.Get("handle") != "no-subscription" || s.body.Get("digest") != "abc" {
			t.Errorf("%s %s %v", s.method, s.path, s.body)
		}
	})

	t.Run("disable repo", func(t *testing.T) {
		c, s := newSpy(t, `{"data":null}`)
		err := c.SetRepoEnabled(ctx, "pve", "/etc/apt/sources.list.d/pve-enterprise.sources", 0, false, "")
		if err != nil {
			t.Fatal(err)
		}
		if s.method != http.MethodPost || s.body.Get("enabled") != "0" || s.body.Get("index") != "0" ||
			s.body.Get("path") != "/etc/apt/sources.list.d/pve-enterprise.sources" || s.body.Has("digest") {
			t.Errorf("%s %v", s.method, s.body)
		}
	})

	t.Run("refresh index", func(t *testing.T) {
		c, s := newSpy(t, okUPID)
		upid, err := c.RefreshAptIndex(ctx, "pve")
		if err != nil || !IsUPID(upid) {
			t.Fatalf("UPID %q, %v", upid, err)
		}
		if s.method != http.MethodPost || s.path != "/api2/json/nodes/pve/apt/update" || s.body.Get("quiet") != "1" {
			t.Errorf("%s %s %v", s.method, s.path, s.body)
		}
	})
}
