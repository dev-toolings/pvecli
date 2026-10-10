// Package aptcheck turns what the node says about its packages into the two
// answers an operator actually wants: « dois-je mettre à jour ? » and « mes
// dépôts tiennent-ils sans abonnement ? ».
//
// Everything here is a pure function of API answers, so the verdict can be
// pinned by tests against real captures (testdata/apt-*.json) without a node.
package aptcheck

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/dev-toolings/pvecli/internal/pve"
)

// RepoRef addresses one stanza the way the API does: file and index.
type RepoRef struct {
	Path  string `json:"path"`
	Index int    `json:"index"`
	URI   string `json:"uri"`
}

// NoSubPlan is what it takes to run the node on the free repositories.
type NoSubPlan struct {
	// Add lists the standard handles to add (or re-enable), in order. The
	// no-subscription repositories are added BEFORE the enterprise ones are
	// disabled, so the node is never left with no Proxmox repository at all.
	Add []string `json:"add"`
	// Disable lists the enabled stanzas that point at enterprise.proxmox.com.
	Disable []RepoRef `json:"disable"`
}

// Empty reports whether the node already runs without the enterprise mirror.
func (p NoSubPlan) Empty() bool { return len(p.Add) == 0 && len(p.Disable) == 0 }

// PlanNoSubscription computes the switch from the enterprise repositories to
// their no-subscription counterparts.
//
// The PVE repository is always made sure of: a node with neither enterprise
// nor no-subscription enabled has no source for pve-manager at all. Ceph is
// only touched when a Ceph enterprise repository is enabled, and then it is
// replaced by the no-subscription repository OF THE SAME RELEASE. Switching
// a Squid node to Tentacle is a major upgrade, not a repository change.
// The « test » repositories are never added.
func PlanNoSubscription(r *pve.AptRepositories) NoSubPlan {
	var plan NoSubPlan

	if s, ok := r.Standard("no-subscription"); !ok || !s.Enabled() {
		if t, ok := r.Standard("test"); !ok || !t.Enabled() {
			plan.Add = append(plan.Add, "no-subscription")
		}
	}
	for _, s := range r.StandardRepos {
		if s.Handle == "enterprise" || !s.Enabled() || !strings.HasSuffix(s.Handle, "-enterprise") {
			continue
		}
		free := strings.TrimSuffix(s.Handle, "enterprise") + "no-subscription"
		if f, ok := r.Standard(free); ok && !f.Enabled() {
			plan.Add = append(plan.Add, free)
		}
	}

	for _, f := range r.Files {
		for i, repo := range f.Repositories {
			if repo.Enabled == 1 && repo.IsEnterprise() {
				plan.Disable = append(plan.Disable, RepoRef{Path: f.Path, Index: i, URI: strings.Join(repo.URIs, " ")})
			}
		}
	}
	return plan
}

// Upgrade is one package whose candidate differs from what is installed.
type Upgrade struct {
	Package   string `json:"package"`
	Origin    string `json:"origin"`
	Installed string `json:"installed"`
	Candidate string `json:"candidate"`
	// Important is true for the packages /apt/versions tracks: the ones
	// Proxmox itself considers worth showing in its own UI.
	Important bool `json:"important"`
}

// Major reports whether the upgrade changes the leading version number
// (ceph 19.2 → 20.2). Within a stable release that is never routine: it is
// usually a repository pointing at the next major series.
func (u Upgrade) Major() bool {
	if u.Installed == "" {
		return false
	}
	a, _ := run(stripEpoch(u.Installed))
	b, _ := run(stripEpoch(u.Candidate))
	return a != "" && isDigit(a[0]) && compareRun(a, b) != 0
}

func stripEpoch(v string) string {
	if _, rest, ok := strings.Cut(v, ":"); ok {
		return rest
	}
	return v
}

// Report is the answer to « dois-je mettre à jour ce nœud ? ».
type Report struct {
	Node string `json:"node"`

	// Pending is every package apt would upgrade. When the token may not
	// read /apt/update (Sys.Modify), it is limited to the important Proxmox
	// packages of /apt/versions, and Partial says so.
	Pending []Upgrade `json:"pending"`
	Partial bool      `json:"partial"`

	// Manager is pve-manager, the package whose version the UI displays.
	Manager Upgrade `json:"manager"`

	// RunningKernel is what the node booted; NewestKernel the most recent
	// kernel installed on disk. They differ after an upgrade that brought a
	// kernel and was not followed by a reboot.
	RunningKernel string `json:"running_kernel"`
	NewestKernel  string `json:"newest_kernel"`
	// KernelUpdate is true when the upgrade itself will install a new kernel.
	KernelUpdate bool `json:"kernel_update"`

	// RestartDaemons is true when pve-manager on disk is newer than what the
	// running daemons were started from (ManagerVersion): an upgrade happened
	// and pveproxy/pvedaemon were not restarted.
	RestartDaemons bool `json:"restart_daemons"`
}

// Majors lists the pending upgrades that change a major version.
func (r Report) Majors() []Upgrade {
	var out []Upgrade
	for _, u := range r.Pending {
		if u.Major() {
			out = append(out, u)
		}
	}
	return out
}

// UpdatesAvailable reports whether apt has something to install.
func (r Report) UpdatesAvailable() bool { return len(r.Pending) > 0 }

// RebootRequired reports whether a newer kernel than the running one is
// already installed.
func (r Report) RebootRequired() bool {
	return r.RunningKernel != "" && r.NewestKernel != "" &&
		CompareVersions(r.NewestKernel, r.RunningKernel) > 0
}

// Verdict is the one-line answer, in the operator's words.
func (r Report) Verdict() string {
	var parts []string
	switch {
	case r.UpdatesAvailable():
		parts = append(parts, fmt.Sprintf("%d mise(s) à jour disponible(s)", len(r.Pending)))
	default:
		parts = append(parts, "à jour")
	}
	if r.RebootRequired() {
		parts = append(parts, "redémarrage requis (noyau "+r.NewestKernel+" installé, "+r.RunningKernel+" en cours)")
	}
	return strings.Join(parts, ", ")
}

// Highlights names what matters inside Pending: the PVE version and the
// kernel. It is what the shell notification prints.
func (r Report) Highlights() []string {
	var out []string
	if r.Manager.Candidate != "" && r.Manager.Candidate != r.Manager.Installed {
		out = append(out, fmt.Sprintf("pve-manager %s → %s", r.Manager.Installed, r.Manager.Candidate))
	}
	if r.KernelUpdate {
		out = append(out, "nouveau noyau")
	}
	if r.RebootRequired() {
		out = append(out, "redémarrage requis")
	}
	return out
}

// kernelMeta is the package that pulls the newest kernel of a series
// (proxmox-kernel-7.0). Its candidate moving is how apt announces a kernel.
var kernelMeta = regexp.MustCompile(`^proxmox-kernel-\d+\.\d+$`)

// kernelImage is one installed kernel ABI (proxmox-kernel-7.0.14-8-pve-signed).
var kernelImage = regexp.MustCompile(`^proxmox-kernel-(\d[^ ]*-pve)(?:-signed)?$`)

// Assess builds the report from /apt/versions and, when the token could read
// it (full=true), /apt/update. The flag is explicit rather than inferred from
// a nil slice: an empty answer from /apt/update means « nothing to do », and
// must never be mistaken for « not allowed to ask ».
func Assess(node string, versions, updates []pve.AptPackage, full bool) Report {
	rep := Report{Node: node, Partial: !full}

	source := updates
	if !full {
		source = versions
	}
	important := map[string]bool{}
	for _, p := range versions {
		important[p.Package] = true
	}
	seen := map[string]bool{}
	for _, p := range source {
		if p.Version == "" || p.Version == p.OldVersion || seen[p.Package] {
			continue
		}
		seen[p.Package] = true
		rep.Pending = append(rep.Pending, Upgrade{
			Package: p.Package, Origin: p.Origin, Installed: p.OldVersion, Candidate: p.Version,
			Important: important[p.Package],
		})
		if kernelMeta.MatchString(p.Package) || (p.OldVersion == "" && kernelImage.MatchString(p.Package)) {
			rep.KernelUpdate = true
		}
	}
	sort.Slice(rep.Pending, func(i, j int) bool {
		a, b := rep.Pending[i], rep.Pending[j]
		if a.Important != b.Important {
			return a.Important
		}
		if (a.Origin == "Proxmox") != (b.Origin == "Proxmox") {
			return a.Origin == "Proxmox"
		}
		return a.Package < b.Package
	})

	for _, p := range versions {
		switch {
		case p.Package == "pve-manager":
			rep.Manager = Upgrade{Package: p.Package, Origin: p.Origin, Installed: p.OldVersion, Candidate: p.Version}
			running, _, _ := strings.Cut(p.ManagerVersion, "/")
			rep.RestartDaemons = running != "" && p.OldVersion != "" && CompareVersions(p.OldVersion, running) > 0
		case p.Package == "proxmox-ve" && p.RunningKernel != "":
			rep.RunningKernel = p.RunningKernel
		}
		if kernelMeta.MatchString(p.Package) && p.Version != "" && p.Version != p.OldVersion {
			rep.KernelUpdate = true
		}
		if m := kernelImage.FindStringSubmatch(p.Package); m != nil && p.CurrentState == "Installed" {
			if rep.NewestKernel == "" || CompareVersions(m[1], rep.NewestKernel) > 0 {
				rep.NewestKernel = m[1]
			}
		}
	}
	return rep
}

// CompareVersions orders two package or kernel versions: negative, zero or
// positive like strings.Compare. Digit runs compare as numbers (7.0.14 is
// newer than 7.0.2, which a string compare gets wrong), everything else
// lexically. It is not dpkg's full algorithm (no epoch, no « ~ »), and does
// not need to be: it only ever compares two versions of the same package.
func CompareVersions(a, b string) int {
	for a != "" || b != "" {
		ra, restA := run(a)
		rb, restB := run(b)
		if c := compareRun(ra, rb); c != 0 {
			return c
		}
		a, b = restA, restB
	}
	return 0
}

func run(s string) (string, string) {
	if s == "" {
		return "", ""
	}
	digit := isDigit(s[0])
	i := 1
	for i < len(s) && isDigit(s[i]) == digit {
		i++
	}
	return s[:i], s[i:]
}

func compareRun(a, b string) int {
	if a != "" && b != "" && isDigit(a[0]) && isDigit(b[0]) {
		a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
		if len(a) != len(b) {
			if len(a) < len(b) {
				return -1
			}
			return 1
		}
	}
	return strings.Compare(a, b)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
