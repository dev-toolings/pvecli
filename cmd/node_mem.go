package cmd

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/dev-toolings/pvecli/internal/output"
	"github.com/dev-toolings/pvecli/internal/pve"
	"github.com/spf13/cobra"
)

// autoBalloonThreshold is the node memory usage above which pvestatd starts
// shrinking ballooned guests toward their floor. It is PVE's own figure, not a
// setting of this tool.
const autoBalloonThreshold = 0.80

// nodeMemVM is one running QEMU guest, reduced to what the report needs.
type nodeMemVM struct {
	VMID    int    `json:"vmid"`
	Name    string `json:"name"`
	Tags    string `json:"tags,omitempty"`
	Max     int64  `json:"max"`
	Floor   int64  `json:"floor"`
	Given   int64  `json:"given"`
	Shrinks bool   `json:"shrinks"`
	AtFloor bool   `json:"at_floor"`
	Swapped int64  `json:"swapped_out,omitempty"`
}

// nodeMemReport is the node's memory commitment, and who pays when it runs out.
type nodeMemReport struct {
	HostTotal int64       `json:"host_total"`
	HostUsed  int64       `json:"host_used"`
	SumMax    int64       `json:"sum_max"`
	SumFloor  int64       `json:"sum_floor"`
	VMs       []nodeMemVM `json:"vms"`
	Warnings  []string    `json:"warnings"`
}

// vmMemFrom reads a guest's ceiling, floor and current allocation. A guest
// with no floor below its ceiling is never shrunk: its floor is its ceiling.
func vmMemFrom(st *pve.GuestStatus) nodeMemVM {
	vm := nodeMemVM{VMID: st.VMID, Name: st.Name, Tags: st.Tags, Max: st.MaxMem, Floor: st.MaxMem, Given: st.MaxMem}
	if st.BalloonMin > 0 && st.BalloonMin < st.MaxMem {
		vm.Floor = st.BalloonMin
		vm.Shrinks = true
	}
	if st.BalloonInfo != nil && st.BalloonInfo.Actual > 0 {
		vm.Given = st.BalloonInfo.Actual
		vm.Swapped = st.BalloonInfo.MemSwappedOut
	}
	vm.AtFloor = st.AtBalloonFloor()
	return vm
}

// buildNodeMemReport is pure: the figures in, the verdicts out.
func buildNodeMemReport(hostTotal, hostUsed int64, vms []nodeMemVM) nodeMemReport {
	report := nodeMemReport{HostTotal: hostTotal, HostUsed: hostUsed, VMs: append([]nodeMemVM(nil), vms...)}
	sort.Slice(report.VMs, func(i, j int) bool { return report.VMs[i].VMID < report.VMs[j].VMID })

	var shrinking []string
	for _, vm := range report.VMs {
		report.SumMax += vm.Max
		report.SumFloor += vm.Floor
		if vm.Shrinks {
			shrinking = append(shrinking, strconv.Itoa(vm.VMID))
		}
	}

	if hostTotal > 0 && report.SumMax > hostTotal {
		report.Warnings = append(report.Warnings, fmt.Sprintf(
			"surengagé : les VM en marche peuvent demander %s pour %s sur l'hôte",
			output.Bytes(report.SumMax), output.Bytes(hostTotal)))
	}
	if hostTotal > 0 && report.SumFloor > hostTotal {
		report.Warnings = append(report.Warnings, fmt.Sprintf(
			"même comprimées à leur plancher, les VM demandent %s pour %s : le ballon ne suffit plus, l'hôte swappe ou tue",
			output.Bytes(report.SumFloor), output.Bytes(hostTotal)))
	}
	if hostTotal > 0 && float64(hostUsed) > autoBalloonThreshold*float64(hostTotal) {
		report.Warnings = append(report.Warnings, fmt.Sprintf(
			"hôte à %s, au-dessus des %s où PVE comprime les VM ballonnées",
			output.Ratio(float64(hostUsed)/float64(hostTotal)), output.Ratio(autoBalloonThreshold)))
	}
	if len(shrinking) > 0 && len(shrinking) < len(report.VMs) {
		report.Warnings = append(report.Warnings, fmt.Sprintf(
			"seules les VM %s ont un plancher : elles absorbent toute la pression de l'hôte, les autres ne cèdent rien",
			strings.Join(shrinking, ", ")))
	}
	for _, vm := range report.VMs {
		if vm.AtFloor {
			report.Warnings = append(report.Warnings, fmt.Sprintf(
				"VM %d (%s) comprimée à son plancher : %s sur %s. Si c'est une production, « pvecli vm set %d --balloon %d » lui rend sa RAM",
				vm.VMID, vm.Name, output.Bytes(vm.Given), output.Bytes(vm.Max), vm.VMID, vm.Max>>20))
		}
	}
	return report
}

func nodeMemRows(r nodeMemReport) output.Rows {
	rows := output.Rows{Headers: []string{"VMID", "NOM", "MAX", "PLANCHER", "DONNÉ", "ÉTAT"}}
	for _, vm := range r.VMs {
		state := "fixe"
		switch {
		case vm.AtFloor:
			state = "au plancher"
		case vm.Given < vm.Max:
			state = "comprimée"
		case vm.Shrinks:
			state = "ballonnée"
		}
		if vm.Swapped > 0 && vm.Given < vm.Max {
			state += ", swap " + output.Bytes(vm.Swapped)
		}
		floor := "—"
		if vm.Shrinks {
			floor = output.Bytes(vm.Floor)
		}
		rows.Cells = append(rows.Cells, []string{strconv.Itoa(vm.VMID), vm.Name,
			output.Bytes(vm.Max), floor, output.Bytes(vm.Given), state})
	}
	rows.Cells = append(rows.Cells, []string{"", "TOTAL", output.Bytes(r.SumMax), output.Bytes(r.SumFloor), "", ""})
	used := "?"
	if r.HostTotal > 0 {
		used = fmt.Sprintf("%s / %s (%s)", output.Bytes(r.HostUsed), output.Bytes(r.HostTotal),
			output.Ratio(float64(r.HostUsed)/float64(r.HostTotal)))
	}
	rows.Cells = append(rows.Cells, []string{"", "HÔTE", used, "", "", ""})
	for _, w := range r.Warnings {
		rows.Cells = append(rows.Cells, []string{"!", w, "", "", "", ""})
	}
	return rows
}

func newNodeMemCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "mem [node]",
		Short: "Engagement mémoire du nœud : qui cède quand l'hôte manque",
		Long: `Additionne la mémoire que les VM en marche peuvent demander, la compare à
l'hôte, et dit qui paie quand elle manque.

Au-dessus de 80 % de mémoire utilisée sur l'hôte, pvestatd gonfle le ballon
des VM qui ont un plancher (clé « balloon » inférieure à « memory ») et les
ramène vers ce plancher. Les VM sans plancher ne cèdent rien. Si une seule VM
de production a un plancher, c'est elle qui absorbe toute la pression du nœud,
et l'invité voit sa RAM fondre sans qu'aucune alerte ne le dise.

Une ligne par VM QEMU en marche : plafond, plancher, RAM réellement donnée par
le ballon, et l'état (fixe, ballonnée, comprimée, au plancher).

Endpoints : GET /nodes/{node}/status, GET /nodes/{node}/qemu,
GET /nodes/{node}/qemu/{vmid}/status/current`,
		Args: usage(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := renderOptions(cmd)
			if err != nil {
				return err
			}
			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			node, err := targetNode(cmd, args)
			if err != nil {
				return err
			}
			status, err := client.NodeStatus(cmd.Context(), node)
			if err != nil {
				return err
			}
			guests, err := client.VMs(cmd.Context(), node)
			if err != nil {
				return err
			}
			var vms []nodeMemVM
			for _, g := range guests {
				if g.Status != "running" {
					continue
				}
				st, err := client.GuestStatus(cmd.Context(), node, pve.TypeQEMU, g.VMID)
				if err != nil {
					return err
				}
				vms = append(vms, vmMemFrom(st))
			}
			report := buildNodeMemReport(status.Memory.Total, status.Memory.Used, vms)
			return output.Render(cmd.OutOrStdout(), opts, report, nodeMemRows(report))
		},
	}
	addRenderFlags(c)
	return c
}
