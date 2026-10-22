package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/dev-toolings/pvecli/internal/aptcheck"
	"github.com/dev-toolings/pvecli/internal/nag"
	"github.com/dev-toolings/pvecli/internal/nodeupgrade"
	"github.com/dev-toolings/pvecli/internal/output"
	"github.com/dev-toolings/pvecli/internal/pve"
	"github.com/spf13/cobra"
)

func newNodeUpgradeCmd() *cobra.Command {
	var (
		reboot bool
		wait   time.Duration
	)

	c := &cobra.Command{
		Use:   "upgrade [node]",
		Short: "Installe les paquets en attente (apt full-upgrade, par SSH)",
		Long: `Installe les paquets en attente sur le nœud.

L'API PVE ne sait pas lancer « apt full-upgrade » : POST /apt/update ne fait
que « apt-get update ». Cette commande passe donc par SSH, comme « node nag » :
le client « ssh » du poste, ta configuration (~/.ssh/config, agent, known_hosts),
BatchMode, et un script envoyé sur l'entrée de « sh -s ».

Le script lance « apt-get update » puis « apt-get full-upgrade ». C'est la même
opération que « apt full-upgrade ». Jamais « apt upgrade » : sur PVE, « upgrade »
peut laisser un noyau en retrait. Les fichiers de configuration déjà modifiés
sur le nœud sont gardés, et debconf ne pose pas de question.

CE N'EST PAS « pvecli upgrade ». Celui-là remplace le binaire de cette CLI.
Celui-ci met à jour Proxmox.

APRÈS. La commande relit le nœud par l'API. S'il y a un noyau plus récent que
celui qui tourne, elle dit qui reviendrait après un redémarrage :

  · un invité en cours sans onboot=1 reste arrêté ;
  · un invité arrêté avec onboot=1 démarre.

Elle ne redémarre pas. Le redémarrage est « pvecli node reboot ». --reboot le
fait dans la foulée, seulement si un noyau plus récent est déjà installé.
La liste onboot est affichée avant la confirmation.

--dry-run affiche le script, sans le lancer et sans lire l'API.`,
		Args: usage(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNodeUpgrade(cmd, args, reboot, wait)
		},
	}

	c.Flags().String("ssh-host", "", "hôte SSH du nœud (défaut : l'hôte de l'endpoint)")
	c.Flags().String("ssh-user", "root", "utilisateur SSH : apt sur le nœud exige root")
	c.Flags().Bool("dry-run", false, "affiche le script qui serait exécuté, sans l'exécuter")
	c.Flags().Bool("yes", false, "ne demande pas confirmation (usage script)")
	c.Flags().BoolVar(&reboot, "reboot", false, "redémarre ensuite, seulement si un noyau plus récent est installé")
	c.Flags().DurationVar(&wait, "wait", 10*time.Minute, "avec --reboot : délai accordé au nœud pour revenir")
	addRenderFlags(c)
	return c
}

func runNodeUpgrade(cmd *cobra.Command, args []string, reboot bool, wait time.Duration) error {
	opts, err := renderOptions(cmd)
	if err != nil {
		return err
	}
	ssh, err := nagSSH(cmd)
	if err != nil {
		return err
	}
	if err := ssh.Look(); err != nil {
		return err
	}

	dryRun, _ := cmd.Flags().GetBool("dry-run")
	yes, _ := cmd.Flags().GetBool("yes")
	if dryRun {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "# à exécuter sur %s via « ssh … sh -s »\n", ssh.Target())
		_, err := fmt.Fprint(cmd.OutOrStdout(), nodeupgrade.Script())
		return err
	}

	// The node name is required once we leave the dry run: the follow-up
	// read and the optional reboot both address one node. Failing here, before
	// apt, is the difference between a usage error and a node left half-upgraded
	// with nobody told.
	node, err := targetNode(cmd, args)
	if err != nil {
		return err
	}
	client, err := newClient(cmd)
	if err != nil {
		return err
	}

	if reboot {
		preview, err := loadBoot(cmd.Context(), client, node)
		if err != nil {
			return fmt.Errorf("impossible de lire les invités avant --reboot : %w\nRien n'a été installé", err)
		}
		writeBootPreview(cmd, preview)
	}

	if !yes {
		question := fmt.Sprintf("lancer apt full-upgrade sur %s ? Le nœud ne redémarre pas.", ssh.Target())
		if reboot {
			question = fmt.Sprintf("lancer apt full-upgrade sur %s, puis redémarrer %s si un noyau plus récent est installé ?", ssh.Target(), node)
		}
		if err := confirm(cmd, question); err != nil {
			return err
		}
	}

	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "apt full-upgrade sur %s\n", ssh.Target())
	if err := ssh.Run(cmd.Context(), nodeupgrade.Script(), cmd.ErrOrStderr(), cmd.ErrOrStderr()); err != nil {
		return err
	}

	rep, err := assessNode(cmd.Context(), client, node)
	if err != nil {
		return fmt.Errorf("apt full-upgrade est terminé sur %s, mais la relecture du nœud a échoué : %w\nLe nœud n'a pas été redémarré", ssh.Target(), err)
	}
	boot, bootErr := loadBoot(cmd.Context(), client, node)

	rebootNow := reboot && bootErr == nil && rep.RebootRequired()
	if err := renderUpgrade(cmd, opts, ssh, node, rep, boot, bootErr, reboot); err != nil {
		return err
	}
	if bootErr != nil && reboot {
		return fmt.Errorf("apt full-upgrade est terminé. La liste onboot est illisible : %w\nLe nœud n'a pas été redémarré", bootErr)
	}
	if !rebootNow {
		return nil
	}

	_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
		"le nœud redémarre, attente de son retour (uptime plus bas), %s au plus", wait)
	cameBack, err := rebootAndWait(cmd.Context(), cmd, client, node, wait)
	_, _ = fmt.Fprintln(cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	rows := output.Rows{Headers: []string{"CHAMP", "VALEUR"}, Cells: [][]string{
		{"nœud", node},
		{"statut", "online"},
		{"détail", "revenu, uptime " + output.Uptime(cameBack.Uptime) + ", noyau " + cameBack.KVersion},
	}}
	return output.Render(cmd.OutOrStdout(), opts, cameBack, rows)
}

func renderUpgrade(cmd *cobra.Command, opts output.Options, ssh nag.SSH, node string, rep aptcheck.Report, boot nodeupgrade.Boot, bootErr error, reboot bool) error {
	stay, will := "illisible", "illisible"
	if bootErr == nil {
		stay = nodeupgrade.Labels(boot.StayDown)
		will = nodeupgrade.Labels(boot.WillStart)
	}
	kernel := rep.RunningKernel
	if rep.NewestKernel != "" && rep.NewestKernel != rep.RunningKernel {
		kernel += " (installé : " + rep.NewestKernel + ")"
	}
	rows := output.Rows{Headers: []string{"CHAMP", "VALEUR"}, Cells: [][]string{
		{"nœud", ssh.Target()},
		{"apt", "full-upgrade terminé"},
		{"noyau", kernel},
		{"restent arrêtés", stay},
		{"vont démarrer", will},
		{"suite", nodeupgrade.NextStep(node, rep.RebootRequired(), reboot && bootErr == nil)},
	}}
	return output.Render(cmd.OutOrStdout(), opts, rep, rows)
}

func writeBootPreview(cmd *cobra.Command, boot nodeupgrade.Boot) {
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
		"invités en cours sans onboot=1, ils resteraient arrêtés : %s\n", nodeupgrade.Labels(boot.StayDown))
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
		"invités arrêtés avec onboot=1, ils démarreraient : %s\n", nodeupgrade.Labels(boot.WillStart))
}

func loadBoot(ctx context.Context, client *pve.Client, node string) (nodeupgrade.Boot, error) {
	var guests []nodeupgrade.Guest
	vms, err := client.VMs(ctx, node)
	if err != nil {
		return nodeupgrade.Boot{}, err
	}
	cts, err := client.Containers(ctx, node)
	if err != nil {
		return nodeupgrade.Boot{}, err
	}
	all := append(vms, cts...)
	for _, g := range all {
		cfg, err := client.GuestConfig(ctx, node, g.Type, g.VMID)
		if err != nil {
			return nodeupgrade.Boot{}, err
		}
		name := g.Name
		if name == "" {
			name = cfg.String("hostname")
		}
		if name == "" {
			name = cfg.String("name")
		}
		guests = append(guests, nodeupgrade.Guest{
			VMID:     g.VMID,
			Name:     name,
			Status:   g.Status,
			OnBoot:   cfg.String("onboot") == "1",
			Template: g.IsTemplate(),
		})
	}
	return nodeupgrade.Classify(guests), nil
}

// rebootAndWait is the proof half of « node reboot », reused so --reboot does
// not invent a second definition of « the node came back ».
func rebootAndWait(ctx context.Context, cmd *cobra.Command, client *pve.Client, node string, wait time.Duration) (*pve.NodeStatus, error) {
	before, err := client.NodeStatus(ctx, node)
	if err != nil {
		return nil, fmt.Errorf("apt full-upgrade est terminé, mais l'uptime d'avant redémarrage est illisible : %w\nLe nœud n'a pas été redémarré", err)
	}
	if err := client.RebootNode(ctx, node); err != nil {
		return nil, fmt.Errorf("apt full-upgrade est terminé, le redémarrage a été refusé : %w", err)
	}
	probe := nodeReturnProbe{
		status:   func(ctx context.Context) (*pve.NodeStatus, error) { return client.NodeStatus(ctx, node) },
		interval: 5 * time.Second,
		timeout:  wait,
		progress: cmd.ErrOrStderr(),
	}
	return probe.wait(ctx, node, before.Uptime)
}
