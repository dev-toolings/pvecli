package pve

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// GuestType tells a QEMU virtual machine from an LXC container.
type GuestType string

const (
	TypeQEMU GuestType = "qemu"
	TypeLXC  GuestType = "lxc"
)

// Guest is one entry of GET /nodes/{node}/qemu or GET /nodes/{node}/lxc.
//
// The two indexes are nearly symmetric, which is what makes one type honest
// here — but they are not identical, and where they diverge is the material
// for decision D4 of the PRD:
//
//   - Template is QEMU-only. A template is a VM carrying a flag, not an object
//     of another kind. Everything about cloning (PVX-024) follows from that.
//   - Unprivileged is LXC-only, and is the security property chapter 03 is about.
//
// Schema verified on the lab node (PVE 9.2.2): the return properties of
// PVE::QemuServer::vmstatus_return_properties, read in the node's own source.
type Guest struct {
	VMID   int    `json:"vmid"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status"`

	// Type is filled by pvecli, not by the API: it is what lets `guest ls`
	// merge both families into one table.
	Type GuestType `json:"type,omitempty"`

	CPUs int     `json:"cpus,omitempty"`
	CPU  float64 `json:"cpu,omitempty"`

	Mem     int64 `json:"mem,omitempty"`
	MaxMem  int64 `json:"maxmem,omitempty"`
	Disk    int64 `json:"disk,omitempty"`
	MaxDisk int64 `json:"maxdisk,omitempty"`
	Uptime  int64 `json:"uptime,omitempty"`

	// Tags is a semicolon-separated list, not an array.
	Tags string `json:"tags,omitempty"`
	// Lock is non-empty while a task holds the guest.
	Lock string `json:"lock,omitempty"`

	Template     int `json:"template,omitempty"`
	Unprivileged int `json:"unprivileged,omitempty"`
}

// IsTemplate reports whether this guest is a template rather than a VM.
func (g Guest) IsTemplate() bool { return g.Template == 1 }

// TagList splits the semicolon-separated tag string.
func (g Guest) TagList() []string {
	if g.Tags == "" {
		return nil
	}
	return strings.Split(g.Tags, ";")
}

// HasTag reports whether the guest carries tag.
func (g Guest) HasTag(tag string) bool { return HasTag(g.Tags, tag) }

// HasTag reports whether a semicolon-separated tag string carries tag.
//
// Exported next to the method because tags arrive from two shapes — a Guest
// from the index, a GuestStatus from status/current — and the `managed`
// ownership guard reads whichever it has.
func HasTag(tags, tag string) bool {
	if tags == "" {
		return false
	}
	for _, t := range strings.Split(tags, ";") {
		if strings.EqualFold(strings.TrimSpace(t), tag) {
			return true
		}
	}
	return false
}

// VMs lists the QEMU guests of a node.
//
// GET /nodes/{node}/qemu
func (c *Client) VMs(ctx context.Context, node string) ([]Guest, error) {
	return c.guests(ctx, epQemuList, node, TypeQEMU)
}

// Containers lists the LXC guests of a node.
//
// GET /nodes/{node}/lxc
func (c *Client) Containers(ctx context.Context, node string) ([]Guest, error) {
	return c.guests(ctx, epLXCList, node, TypeLXC)
}

func (c *Client) guests(ctx context.Context, e endpoint, node string, kind GuestType) ([]Guest, error) {
	var out []Guest
	if err := c.get(ctx, e, []string{node}, nil, &out); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Type = kind
	}
	// PVE returns the index in hash order, which changes between calls. A
	// listing that reshuffles itself is unusable in a diff.
	sort.Slice(out, func(i, j int) bool { return out[i].VMID < out[j].VMID })
	return out, nil
}

// GuestConfig is the configuration of one guest, as PVE stores it: a flat map
// whose keys are dynamic (net0, net1, scsi0, …) and whose values are often
// option strings.
type GuestConfig map[string]any

// String returns a configuration value as text, whatever JSON type it arrived
// as: PVE answers integers, strings and booleans in the same map.
func (g GuestConfig) String(key string) string {
	v, ok := g[key]
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "1"
		}
		return "0"
	default:
		return fmt.Sprint(t)
	}
}

// KeysWithPrefix returns the sorted configuration keys starting with prefix —
// "net" for interfaces, "scsi"/"virtio"/"sata"/"ide" for disks.
func (g GuestConfig) KeysWithPrefix(prefix string) []string {
	var keys []string
	for k := range g {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		// "net0" yes, "nettoyage" no: what follows the prefix must be an index.
		if _, err := strconv.Atoi(strings.TrimPrefix(k, prefix)); err == nil {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// GuestConfig reads a guest's configuration.
//
// GET /nodes/{node}/qemu/{vmid}/config  ·  GET /nodes/{node}/lxc/{vmid}/config
func (c *Client) GuestConfig(ctx context.Context, node string, kind GuestType, vmid int) (GuestConfig, error) {
	e := epQemuConfig
	if kind == TypeLXC {
		e = epLXCConfig
	}
	var out GuestConfig
	if err := c.get(ctx, e, []string{node, strconv.Itoa(vmid)}, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GuestStatus is GET /nodes/{node}/{kind}/{vmid}/status/current.
type GuestStatus struct {
	VMID   int     `json:"vmid"`
	Name   string  `json:"name,omitempty"`
	Status string  `json:"status"`
	CPUs   int     `json:"cpus,omitempty"`
	CPU    float64 `json:"cpu,omitempty"`
	Mem    int64   `json:"mem,omitempty"`
	MaxMem int64   `json:"maxmem,omitempty"`
	Uptime int64   `json:"uptime,omitempty"`
	Lock   string  `json:"lock,omitempty"`
	Tags   string  `json:"tags,omitempty"`
	PID    int     `json:"pid,omitempty"`

	// QMPStatus is the QEMU monitor's own view. It can say "paused" while
	// Status says "running" — the difference matters when waiting on a guest.
	QMPStatus string `json:"qmpstatus,omitempty"`

	// Agent is 1 when the QEMU guest agent is declared in the config. It says
	// nothing about whether it is actually answering.
	Agent int `json:"agent,omitempty"`

	// FreeMem is the guest's own MemFree, relayed by the virtio-balloon driver.
	// It is the counterweight to Mem, which PVE computes as total_mem minus
	// free_mem: the guest's page cache is therefore counted as used, and any
	// healthy container host reads as full.
	//
	// MemAvailable, the one figure that answers "is it actually tight?", does
	// cross the virtio boundary: the driver reports it as stat-available-memory
	// and QEMU exposes it on the balloon device, next to stat-disk-caches. It
	// is PVE that drops both, keeping only total and free out of the stat block
	// (PVE::QemuServer, the query-balloon callback). Reaching it therefore
	// means going around the status endpoint, which is what `vm mem` does.
	FreeMem int64 `json:"freemem,omitempty"`

	// MemHost is what the QEMU process really occupies on the node. Once the
	// guest touches a page, the host allocates it for good and cannot take it
	// back short of inflating the balloon, so this stays near MaxMem even when
	// the guest considers the page reclaimable. Absent on LXC.
	MemHost int64 `json:"memhost,omitempty"`

	// PressureMemorySome and PressureMemoryFull are the cgroup's memory PSI
	// counters, in percent of time stalled. They are the only fields here that
	// say whether memory hurts. Pointers, because zero is their normal reading
	// and "no pressure" must not be indistinguishable from "not reported".
	PressureMemorySome *flexFloat `json:"pressurememorysome,omitempty"`
	PressureMemoryFull *flexFloat `json:"pressurememoryfull,omitempty"`
}

// flexFloat is a number PVE answers as a JSON number from one endpoint and as
// a quoted string from another.
//
// Observed on the lab, on the very same field: GET …/qemu/250/status/current
// returns {"pressurememorysome":0}, while …/lxc/221/status/current returns
// {"pressurememorysome":"0.00"}. A strict float decode fails on containers
// only, so the bug hides behind whichever guest type gets tested first.
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(raw []byte) error {
	text := strings.Trim(string(raw), `"`)
	if text == "" || text == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return fmt.Errorf("nombre attendu, reçu %s", raw)
	}
	*f = flexFloat(v)
	return nil
}

// Float returns the value as a plain float.
func (f flexFloat) Float() float64 { return float64(f) }

// GuestStatus reads a guest's current runtime state.
//
// GET /nodes/{node}/qemu/{vmid}/status/current
func (c *Client) GuestStatus(ctx context.Context, node string, kind GuestType, vmid int) (*GuestStatus, error) {
	e := epQemuStatus
	if kind == TypeLXC {
		e = epLXCStatus
	}
	var out GuestStatus
	if err := c.get(ctx, e, []string{node, strconv.Itoa(vmid)}, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
