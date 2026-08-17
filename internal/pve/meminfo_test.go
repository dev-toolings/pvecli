package pve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

// labMeminfo reads the captured /proc/meminfo out of the same fixture the
// command tests replay.
func labMeminfo(t *testing.T) Meminfo {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/agent-meminfo.json")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Data struct {
			Content string `json:"content"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	info, err := ParseMeminfo(body.Data.Content)
	if err != nil {
		t.Fatalf("fixture illisible : %v", err)
	}
	return info
}

// The counters must come back in bytes, since every other size in this package
// is in bytes and /proc/meminfo is the only source that speaks kB.
func TestParseMeminfoConvertsToBytes(t *testing.T) {
	info := labMeminfo(t)

	if info.Total < 7<<30 || info.Total > 8<<30 {
		t.Errorf("Total = %d, attendu de l'ordre de 8 GiB", info.Total)
	}
	if info.Available <= info.Free {
		t.Errorf("Available (%d) doit dépasser Free (%d) : c'est tout l'intérêt du compteur",
			info.Available, info.Free)
	}
	if info.Cache() <= 0 {
		t.Error("le cache récupérable ne peut pas être nul sur cette capture")
	}
}

// Used follows procps, total minus available. The older total minus free minus
// cache disagrees by the pinned share of the cache, and understates usage.
func TestMeminfoUsedIsTotalMinusAvailable(t *testing.T) {
	info := labMeminfo(t)

	if got, want := info.Used(), info.Total-info.Available; got != want {
		t.Errorf("Used = %d, want %d", got, want)
	}
	if info.Used() >= info.Total-info.Free {
		t.Error("Used doit rester sous total moins libre : le cache n'est pas de l'utilisé")
	}
}

// "Cached" and "SwapCached" differ by one prefix. Matching on a prefix would
// silently add a swap counter to the page cache.
func TestParseMeminfoMatchesKeysExactly(t *testing.T) {
	info, err := ParseMeminfo("MemTotal: 1024 kB\nCached: 512 kB\nSwapCached: 256 kB\n")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.Cached, int64(512*1024); got != want {
		t.Errorf("Cached = %d, want %d — SwapCached a fuité dedans", got, want)
	}
}

// An unknown key is not an error: /proc/meminfo gains lines with every kernel
// release, and refusing to parse a newer guest would be the wrong trade.
func TestParseMeminfoIgnoresUnknownKeys(t *testing.T) {
	info, err := ParseMeminfo("MemTotal: 2048 kB\nUneCleQuiNexistePasEncore: 1 kB\nMemAvailable: 1024 kB\n")
	if err != nil {
		t.Fatal(err)
	}
	if info.Total != 2048*1024 || info.Available != 1024*1024 {
		t.Errorf("clé inconnue mal absorbée : %+v", info)
	}
}

// Something that is not a /proc/meminfo must fail loudly. A zeroed struct would
// render as a guest with no memory at all, which reads like an incident.
func TestParseMeminfoRejectsForeignContent(t *testing.T) {
	if _, err := ParseMeminfo("root:x:0:0::/root:/bin/bash\n"); !errors.Is(err, ErrMeminfoUnreadable) {
		t.Errorf("err = %v, want ErrMeminfoUnreadable", err)
	}
}

// Ratio must not divide by zero when the guest reported nothing.
func TestMeminfoRatioToleratesAnEmptyTotal(t *testing.T) {
	if got := (Meminfo{}).Ratio(42); got != 0 {
		t.Errorf("Ratio = %v, want 0", got)
	}
}

// The file must be requested as a GET carrying `file` in the query string. PVE
// answers 501 to the POST its sibling agent/exec uses, and a 501 reads like an
// endpoint the node does not have rather than like a wrong verb.
func TestAgentMeminfoAsksForProcMeminfoByQuery(t *testing.T) {
	var gotMethod, gotPath, gotFile string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotFile = r.URL.Query().Get("file")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"bytes-read":22,"content":"MemTotal: 1024 kB\n"}}`))
	})

	if _, err := c.AgentMeminfo(context.Background(), "pve", 230); err != nil {
		t.Fatalf("AgentMeminfo: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("méthode = %s, want GET", gotMethod)
	}
	if want := "/api2/json/nodes/pve/qemu/230/agent/file-read"; gotPath != want {
		t.Errorf("chemin = %s, want %s", gotPath, want)
	}
	if gotFile != "/proc/meminfo" {
		t.Errorf("paramètre file = %q, want /proc/meminfo", gotFile)
	}
}

// PVE answers 500 for a missing agent, a stopped guest and a real fault alike.
// Translating it is the difference between a message that names the package to
// install and one that looks like the hypervisor is broken.
func TestAgentMeminfoTranslatesAMissingAgent(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"data":null,"errors":{"agent":"QEMU guest agent is not running"}}`))
	})

	_, err := c.AgentMeminfo(context.Background(), "pve", 230)
	if !errors.Is(err, ErrAgentUnavailable) {
		t.Fatalf("err = %v, want ErrAgentUnavailable", err)
	}
	if !strings.Contains(err.Error(), "qemu-guest-agent") {
		t.Errorf("le message n'oriente pas vers le paquet manquant : %v", err)
	}
}

// A 403 is not an absent agent and must keep its own meaning: the /monitor
// route needs Sys.Audit, this one needs VM.GuestAgent.FileRead, and confusing
// a permission gap with a missing package sends the operator to the wrong host.
func TestAgentMeminfoLeavesAPermissionErrorAlone(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"data":null,"errors":{"path":"Permission check failed"}}`))
	})

	_, err := c.AgentMeminfo(context.Background(), "pve", 230)
	if err == nil {
		t.Fatal("un 403 doit remonter")
	}
	if errors.Is(err, ErrAgentUnavailable) {
		t.Errorf("403 traduit à tort en agent absent : %v", err)
	}
}

// Guard for the endpoint table: the path must stay declared rather than typed
// at the call site, which is what PRD §6.3 makes checkable.
func TestAgentFileReadEndpointIsDeclared(t *testing.T) {
	if got := epQemuAgentFileRead.Path("pve", "230"); got != "/nodes/pve/qemu/230/agent/file-read" {
		t.Errorf("Path = %s", got)
	}
	if epQemuAgentFileRead.Method != http.MethodGet {
		t.Errorf("méthode = %s, want GET", epQemuAgentFileRead.Method)
	}
	// url is imported for the params type the client builds; keep the guard
	// honest by exercising it.
	if v := (url.Values{"file": {"/proc/meminfo"}}).Encode(); v != "file=%2Fproc%2Fmeminfo" {
		t.Errorf("encodage du paramètre = %s", v)
	}
}
