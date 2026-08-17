package cmd

import (
	"fmt"
	"strconv"

	"github.com/dev-toolings/pvecli/internal/output"
	"github.com/dev-toolings/pvecli/internal/pve"
	"github.com/spf13/cobra"
)

func newVMMemCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "mem <vmid>",
		Short: "Mémoire réellement disponible dans la VM",
		Long: `Confronte la mémoire telle que Proxmox la compte à celle que l'invité voit.

Les deux ne disent pas la même chose, et l'écart n'est pas une anomalie.
Proxmox dérive son chiffre de virtio-balloon en faisant « total moins libre »,
si bien que le cache de pages compte comme utilisé : un hôte Docker au repos
affiche 90 % et ne manque de rien. Le noyau invité, lui, publie MemAvailable,
son estimation de ce qu'une nouvelle allocation obtiendrait sans swapper.

C'est cette estimation que cette commande va chercher, en lisant /proc/meminfo
par l'agent invité. Le détour est nécessaire : le pilote balloon transmet bien
la valeur, mais PVE ne la retient pas et son endpoint status/current ne l'expose
nulle part.

« mémoire hôte » reste élevée pour une autre raison, qui n'est pas un problème
non plus : une page touchée une fois est allouée pour de bon côté hôte, et seul
le gonflage du ballon la reprend. La pression PSI est le seul compteur qui dit
si l'invité souffre.

Nécessite l'agent invité. Un conteneur LXC n'en a pas.

Endpoint : GET /api2/json/nodes/{node}/qemu/{vmid}/agent/file-read`,
		Args: usage(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			vmid, err := strconv.Atoi(args[0])
			if err != nil {
				return &exitError{code: pve.ExitUsage, msg: fmt.Sprintf("vmid invalide : %q", args[0])}
			}
			opts, err := renderOptions(cmd)
			if err != nil {
				return err
			}
			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			node, err := targetNode(cmd, nil)
			if err != nil {
				return err
			}

			st, err := client.GuestStatus(cmd.Context(), node, pve.TypeQEMU, vmid)
			if err != nil {
				return err
			}
			info, err := client.AgentMeminfo(cmd.Context(), node, vmid)
			if err != nil {
				return err
			}

			data := map[string]any{
				"status": st,
				"guest": map[string]any{
					"total":     info.Total,
					"free":      info.Free,
					"available": info.Available,
					"used":      info.Used(),
					"cache":     info.Cache(),
				},
			}
			return output.Render(cmd.OutOrStdout(), opts, data, memoryRows(st, info))
		},
	}

	addRenderFlags(c)
	return c
}

// memoryRows puts the two readings side by side rather than replacing one with
// the other. Showing only the truthful figure would leave the operator unable
// to reconcile it with the console, which keeps displaying the other one.
func memoryRows(st *pve.GuestStatus, info pve.Meminfo) output.Rows {
	rows := output.Rows{Headers: []string{"CHAMP", "VALEUR"}}
	add := func(k, v string) {
		if v != "" {
			rows.Cells = append(rows.Cells, []string{k, v})
		}
	}

	add("vmid", strconv.Itoa(st.VMID))
	add("nom", st.Name)

	if st.MaxMem > 0 {
		add("vue Proxmox", fmt.Sprintf("%s / %s (%s)",
			output.Bytes(st.Mem), output.Bytes(st.MaxMem),
			output.Ratio(float64(st.Mem)/float64(st.MaxMem))))
	}
	add("réellement utilisé", fmt.Sprintf("%s (%s)",
		output.Bytes(info.Used()), output.Ratio(info.Ratio(info.Used()))))
	add("disponible", fmt.Sprintf("%s (%s)",
		output.Bytes(info.Available), output.Ratio(info.Ratio(info.Available))))
	add("cache récupérable", output.Bytes(info.Cache()))
	add("libre au sens strict", output.Bytes(info.Free))
	add("total vu par l'invité", output.Bytes(info.Total))

	if st.MemHost > 0 {
		add("mémoire hôte", output.Bytes(st.MemHost))
	}
	if p := st.PressureMemorySome; p != nil {
		line := output.Percent(p.Float())
		if f := st.PressureMemoryFull; f != nil && f.Float() > 0 {
			line += " (bloqué " + output.Percent(f.Float()) + ")"
		}
		add("pression mémoire", line)
	}

	return rows
}
