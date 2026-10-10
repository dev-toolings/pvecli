package pve

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

// Le gestionnaire de paquets du nœud, vu par l'API.
//
// Schéma vérifié contre le nœud du lab en PVE 9.2.6 le 10-10-2026
// (« pvesh usage /nodes/pve/apt/… --verbose », captures réelles dans
// testdata/apt-*.json, privilèges lus dans PVE::API2::APT).
//
// Deux pièges structurent ce fichier :
//
//   - LISTER les mises à jour en attente (GET /apt/update) exige Sys.Modify,
//     comme les déclencher. Seuls /apt/versions et /apt/repositories se
//     contentent de Sys.Audit. Un token en lecture seule n'obtient donc
//     qu'un 403 sur la question « y a-t-il des mises à jour ? », alors qu'il
//     peut lire la réponse pour les paquets Proxmox importants.
//   - Les booléens arrivent en ENTIERS (« Enabled »: 1), et « status » d'un
//     dépôt standard est ABSENT quand le dépôt n'est pas configuré : absent,
//     0 et 1 sont trois états distincts, pas deux.

// AptRepoOption is one « Key: Values » line of a repository stanza.
type AptRepoOption struct {
	Key    string   `json:"Key"`
	Values []string `json:"Values"`
}

// AptRepository is one stanza of a .list or .sources file.
type AptRepository struct {
	Types      []string        `json:"Types"`
	URIs       []string        `json:"URIs"`
	Suites     []string        `json:"Suites"`
	Components []string        `json:"Components"`
	Options    []AptRepoOption `json:"Options"`
	Comment    string          `json:"Comment"`
	FileType   string          `json:"FileType"`
	Enabled    int             `json:"Enabled"`
}

// IsEnterprise reports whether the stanza points at the subscription-only
// mirror. The host is the test, not the component name: a hand-written
// stanza can call its component anything, it cannot fetch from
// enterprise.proxmox.com without a key.
func (r AptRepository) IsEnterprise() bool {
	for _, raw := range r.URIs {
		u, err := url.Parse(raw)
		if err == nil && strings.EqualFold(u.Hostname(), "enterprise.proxmox.com") {
			return true
		}
	}
	return false
}

// AptRepoFile is one file under /etc/apt/sources.list{,.d}.
type AptRepoFile struct {
	Path         string          `json:"path"`
	FileType     string          `json:"file-type"`
	Repositories []AptRepository `json:"repositories"`
}

// AptRepoError is a file the node could not parse.
type AptRepoError struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// AptRepoInfo is a remark the node attaches to one stanza: its origin, or a
// warning (wrong suite, missing component…).
type AptRepoInfo struct {
	Path     string `json:"path"`
	Index    int    `json:"index"`
	Kind     string `json:"kind"`
	Message  string `json:"message"`
	Property string `json:"property"`
}

// AptStandardRepo is one of the repositories Proxmox knows by handle.
type AptStandardRepo struct {
	Handle      string `json:"handle"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Status is nil when the repository is not configured at all, 0 when it
	// is configured and disabled, 1 when it is enabled.
	Status *int `json:"status"`
}

// Enabled reports whether the standard repository is configured AND enabled.
func (s AptStandardRepo) Enabled() bool { return s.Status != nil && *s.Status == 1 }

// AptRepositories is GET /nodes/{node}/apt/repositories.
type AptRepositories struct {
	// Digest covers every file. Sending it back with a write makes the node
	// refuse the write if anything changed since this read.
	Digest        string            `json:"digest"`
	Files         []AptRepoFile     `json:"files"`
	Errors        []AptRepoError    `json:"errors"`
	Infos         []AptRepoInfo     `json:"infos"`
	StandardRepos []AptStandardRepo `json:"standard-repos"`
}

// Standard returns the standard repository with that handle, if the node
// lists it.
func (r *AptRepositories) Standard(handle string) (AptStandardRepo, bool) {
	for _, s := range r.StandardRepos {
		if s.Handle == handle {
			return s, true
		}
	}
	return AptStandardRepo{}, false
}

// AptRepositories reads the APT configuration of a node.
//
// GET /nodes/{node}/apt/repositories, Sys.Audit on /nodes/{node}
func (c *Client) AptRepositories(ctx context.Context, node string) (*AptRepositories, error) {
	var out AptRepositories
	if err := c.get(ctx, epAptRepos, []string{node}, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AddStandardRepo adds the standard repository named by handle
// (« no-subscription », « ceph-squid-no-subscription »…). The node writes the
// stanza itself, in the format and file it considers canonical.
//
// PUT /nodes/{node}/apt/repositories, Sys.Modify on /nodes/{node}
func (c *Client) AddStandardRepo(ctx context.Context, node, handle, digest string) error {
	body := url.Values{"handle": {handle}}
	if digest != "" {
		body.Set("digest", digest)
	}
	return c.post(ctx, epAptRepoAdd, []string{node}, body, nil)
}

// SetRepoEnabled enables or disables one stanza, addressed by its file and
// its index within that file. It is the only change this endpoint allows:
// the stanza stays in the file, commented out or flagged « Enabled: false ».
//
// POST /nodes/{node}/apt/repositories, Sys.Modify on /nodes/{node}
func (c *Client) SetRepoEnabled(ctx context.Context, node, path string, index int, enabled bool, digest string) error {
	body := url.Values{
		"path":    {path},
		"index":   {strconv.Itoa(index)},
		"enabled": {boolParam(enabled)},
	}
	if digest != "" {
		body.Set("digest", digest)
	}
	return c.post(ctx, epAptRepoChange, []string{node}, body, nil)
}

// AptPackage is one entry of /apt/update or /apt/versions.
//
// OldVersion is the INSTALLED version, Version the candidate: the naming is
// apt's, read from the point of view of an upgrade. In /apt/update a package
// that is not installed yet (a new kernel ABI) has no OldVersion at all.
type AptPackage struct {
	Package      string `json:"Package"`
	Title        string `json:"Title"`
	Origin       string `json:"Origin"`
	Priority     string `json:"Priority"`
	Section      string `json:"Section"`
	Arch         string `json:"Arch"`
	OldVersion   string `json:"OldVersion"`
	Version      string `json:"Version"`
	CurrentState string `json:"CurrentState"`

	// RunningKernel is set on the « proxmox-ve » entry of /apt/versions only.
	RunningKernel string `json:"RunningKernel"`
	// ManagerVersion is set on the « pve-manager » entry of /apt/versions
	// only: the version the running daemons were started from.
	ManagerVersion string `json:"ManagerVersion"`
}

// AptUpdates lists the packages apt would upgrade, from the package index
// as the node last refreshed it. It does NOT refresh that index: see
// RefreshAptIndex.
//
// GET /nodes/{node}/apt/update, Sys.Modify on /nodes/{node}, not Sys.Audit
func (c *Client) AptUpdates(ctx context.Context, node string) ([]AptPackage, error) {
	var out []AptPackage
	if err := c.get(ctx, epAptUpdates, []string{node}, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// RefreshAptIndex runs « apt-get update » on the node and returns the UPID of
// that task. quiet=1 keeps progress bars out of the task log; notify stays at
// its default (0): the operator is asking, nobody needs an e-mail about it.
//
// POST /nodes/{node}/apt/update, Sys.Modify on /nodes/{node}
func (c *Client) RefreshAptIndex(ctx context.Context, node string) (string, error) {
	var upid string
	err := c.post(ctx, epAptRefresh, []string{node}, url.Values{"quiet": {"1"}}, &upid)
	return upid, err
}

// AptVersions lists the versions of the packages Proxmox considers
// important: installed and candidate, plus the running kernel.
//
// GET /nodes/{node}/apt/versions, Sys.Audit on /nodes/{node}
func (c *Client) AptVersions(ctx context.Context, node string) ([]AptPackage, error) {
	var out []AptPackage
	if err := c.get(ctx, epAptVersions, []string{node}, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func boolParam(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
