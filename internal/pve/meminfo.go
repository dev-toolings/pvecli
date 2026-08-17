package pve

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Meminfo is the guest's own memory accounting, read from its /proc/meminfo.
//
// It exists because PVE's `mem` cannot answer the only question worth asking,
// "is this guest short on memory". PVE derives that figure from virtio-balloon
// as total minus free, so every reclaimable page counts as used: a healthy
// Docker host at rest reads 90 % and is under no pressure whatsoever.
//
// MemAvailable is the kernel's own estimate of what a new allocation could
// obtain without swapping. It already accounts for the fact that most of the
// page cache can be dropped on demand, and no other counter here replaces it:
// free plus cache is an approximation the kernel explicitly declines to make.
type Meminfo struct {
	Total        int64
	Free         int64
	Available    int64
	Buffers      int64
	Cached       int64
	SReclaimable int64
}

// Used mirrors what procps `free` prints in its used column, total minus
// available, rather than the older total minus free minus cache. The two
// disagree by the share of cache the kernel considers pinned, and the kernel's
// own arithmetic is the one to trust.
func (m Meminfo) Used() int64 { return m.Total - m.Available }

// Cache is the reclaimable part: page cache, buffers, and the slab the kernel
// is willing to give back. It is the whole of what inflates PVE's reading.
func (m Meminfo) Cache() int64 { return m.Buffers + m.Cached + m.SReclaimable }

// Ratio returns n as a fraction of total, or 0 when total is unknown.
func (m Meminfo) Ratio(n int64) float64 {
	if m.Total <= 0 {
		return 0
	}
	return float64(n) / float64(m.Total)
}

// ErrMeminfoUnreadable is returned when the agent answered but the payload
// carried none of the fields that make it a /proc/meminfo.
var ErrMeminfoUnreadable = errors.New("/proc/meminfo illisible")

// ParseMeminfo reads the "Key:   1234 kB" lines. Unknown keys are ignored:
// /proc/meminfo grows with every kernel release and a new line is not an error.
func ParseMeminfo(content string) (Meminfo, error) {
	var m Meminfo
	targets := map[string]*int64{
		"MemTotal":     &m.Total,
		"MemFree":      &m.Free,
		"MemAvailable": &m.Available,
		"Buffers":      &m.Buffers,
		"Cached":       &m.Cached,
		"SReclaimable": &m.SReclaimable,
	}

	for _, line := range strings.Split(content, "\n") {
		key, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		// "Cached" and "SwapCached" are different counters, so the key must
		// match exactly rather than by prefix.
		target, wanted := targets[strings.TrimSpace(key)]
		if !wanted {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		value, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		// Every line of interest is in kB. A unit-less line would be a page
		// count, and none of the keys above is ever emitted that way.
		if len(fields) > 1 && !strings.EqualFold(fields[1], "kB") {
			continue
		}
		*target = value * 1024
	}

	if m.Total == 0 {
		return Meminfo{}, ErrMeminfoUnreadable
	}
	return m, nil
}

// AgentMeminfo reads /proc/meminfo through the QEMU guest agent.
//
// The agent is used rather than SSH for the reason the rest of this package
// uses it: it needs no account, no key, no open port and no working guest
// network. It also needs no extra privilege here, VM.GuestAgent.FileRead
// covers it, where the /monitor endpoint that exposes the same figure asks for
// Sys.Audit on the VM path.
//
// GET /nodes/{node}/qemu/{vmid}/agent/file-read
func (c *Client) AgentMeminfo(ctx context.Context, node string, vmid int) (Meminfo, error) {
	var out struct {
		Content   string `json:"content"`
		BytesRead int64  `json:"bytes-read"`
	}

	params := url.Values{"file": {"/proc/meminfo"}}
	if err := c.get(ctx, epQemuAgentFileRead, []string{node, strconv.Itoa(vmid)}, params, &out); err != nil {
		// Same translation as AgentInterfaces: PVE answers 500 for a missing
		// agent, a stopped guest and an agent that is merely not running.
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusInternalServerError {
			return Meminfo{}, &AgentError{VMID: vmid, Err: err}
		}
		return Meminfo{}, err
	}

	info, err := ParseMeminfo(out.Content)
	if err != nil {
		return Meminfo{}, fmt.Errorf("VM %d : %w", vmid, err)
	}
	return info, nil
}
