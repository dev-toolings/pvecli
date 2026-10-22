package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dev-toolings/pvecli/internal/aptcheck"
	"github.com/dev-toolings/pvecli/internal/output"
	"github.com/dev-toolings/pvecli/internal/pve"
	"github.com/dev-toolings/pvecli/internal/service"
	"github.com/spf13/cobra"
)

// Exit codes of « node updates --exit-code ». Above the error range on
// purpose (1 to 5 are taken, see internal/pve/errors.go): a monitoring script
// must be able to tell « the node needs care » from « the check failed ».
const (
	exitUpdatesAvailable = 10
	exitRebootRequired   = 11
)

func newNodeRepoCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "repo",
		Short: "Lit et bascule les dépôts APT du nœud (enterprise ↔ no-subscription)",
		Long: `Les dépôts APT du nœud, par l'API : pas de SSH, contrairement à « node nag ».

Proxmox VE est libre (AGPLv3) et complet sans abonnement. L'abonnement donne
accès au dépôt « pve-enterprise » (paquets plus éprouvés) et au support. Un
nœud fraîchement installé a pourtant ce dépôt activé : sans clé, « apt update »
y échoue en 401 et le nœud ne reçoit plus AUCUNE mise à jour Proxmox, sans
autre symptôme qu'une ligne rouge dans l'interface.

  pvecli node repo ls                 ce qui est configuré, et ce qui cloche
  pvecli node repo no-subscription    bascule sur les dépôts gratuits

Voir aussi « pvecli node nag off » pour la pop-up, et « pvecli node updates »
pour savoir s'il faut mettre le nœud à jour.`,
		Args: usage(cobra.NoArgs),
	}
	c.AddCommand(newNodeRepoListCmd(), newNodeRepoNoSubCmd())
	return c
}

func newNodeRepoListCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "ls [node]",
		Aliases: []string{"list"},
		Short:   "Liste les dépôts APT du nœud (GET /nodes/{node}/apt/repositories)",
		Long: `Affiche chaque dépôt configuré (fichier, index, état, URI), puis les dépôts
standard de Proxmox et ce que le nœud en dit.

La dernière ligne répond à la question qui compte sans abonnement : un dépôt
enterprise est-il encore actif ? Si oui, « pvecli node repo no-subscription ».

PRIVILÈGE : Sys.Audit sur /nodes/{node}.

Endpoint : GET /api2/json/nodes/{node}/apt/repositories`,
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
			repos, err := client.AptRepositories(cmd.Context(), node)
			if err != nil {
				return err
			}
			if err := output.Render(cmd.OutOrStdout(), opts, repos, repoRows(repos)); err != nil {
				return err
			}
			if opts.Format == output.Table {
				for _, e := range repos.Errors {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "erreur  %s : %s\n", e.Path, e.Error)
				}
				for _, i := range repos.Infos {
					if i.Kind == "warning" {
						_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "alerte  %s #%d : %s\n", i.Path, i.Index, i.Message)
					}
				}
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), repoVerdict(repos))
			}
			return nil
		},
	}
	addRenderFlags(c)
	return c
}

func repoRows(r *pve.AptRepositories) output.Rows {
	rows := output.Rows{Headers: []string{"FICHIER", "INDEX", "ÉTAT", "TYPE", "URI", "SUITES", "COMPOSANTS"}}
	for _, f := range r.Files {
		for i, repo := range f.Repositories {
			state := "inactif"
			if repo.Enabled == 1 {
				state = "actif"
			}
			if repo.IsEnterprise() {
				state += " (enterprise)"
			}
			rows.Cells = append(rows.Cells, []string{
				f.Path, strconv.Itoa(i), state,
				strings.Join(repo.Types, " "), strings.Join(repo.URIs, " "),
				strings.Join(repo.Suites, " "), strings.Join(repo.Components, " "),
			})
		}
	}
	return rows
}

// repoVerdict is the one line `repo ls` ends on.
func repoVerdict(r *pve.AptRepositories) string {
	plan := aptcheck.PlanNoSubscription(r)
	if plan.Empty() {
		return "sans abonnement : OK (aucun dépôt enterprise actif, dépôt no-subscription présent)"
	}
	var why []string
	if len(plan.Disable) > 0 {
		why = append(why, fmt.Sprintf("%d dépôt(s) enterprise actif(s) (apt update y échoue sans clé)", len(plan.Disable)))
	}
	if len(plan.Add) > 0 {
		why = append(why, "à ajouter : "+strings.Join(plan.Add, ", "))
	}
	return "sans abonnement : À CORRIGER. " + strings.Join(why, " ; ") +
		"\n  pvecli node repo no-subscription"
}

func newNodeRepoNoSubCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "no-subscription [node]",
		Aliases: []string{"nosub"},
		Short:   "Bascule le nœud sur les dépôts gratuits (sans abonnement)",
		Long: `Ajoute le dépôt « no-subscription », puis désactive tout dépôt qui pointe sur
enterprise.proxmox.com. Dans cet ordre : le nœud n'est jamais sans dépôt
Proxmox, même si la seconde étape échoue.

Ceph suit la même règle, À VERSION ÉGALE : un « ceph-squid-enterprise » actif
est remplacé par « ceph-squid-no-subscription », jamais par une autre version
(changer de version de Ceph est une montée de version majeure, pas une affaire
de dépôt). Le dépôt « test » n'est jamais ajouté.

Ce n'est pas un contournement : « no-subscription » est le dépôt public que
Proxmox publie pour l'usage sans abonnement. Ses paquets sont moins éprouvés
que ceux d'enterprise ; sur un homelab c'est le choix normal, en production
l'abonnement Community reste la voie propre.

Les dépôts désactivés restent dans leurs fichiers (« Enabled: false ») :
rien n'est supprimé, et le plan imprime la commande exacte pour revenir.
Idempotent : sur un nœud déjà basculé, rien n'est écrit.

La pop-up « No valid subscription » n'est PAS concernée : elle ne dépend pas
des dépôts. Voir « pvecli node nag off ».

PRIVILÈGE : Sys.Modify sur /nodes/{node}.

Endpoints : PUT  /api2/json/nodes/{node}/apt/repositories  (handle=…)
            POST /api2/json/nodes/{node}/apt/repositories  (path, index, enabled=0)`,
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

			// Read once up front: the plan printed, the confirmation asked
			// and the writes sent must all describe the same state. The
			// digest makes the node refuse the first write if it changed.
			before, err := client.AptRepositories(cmd.Context(), node)
			if err != nil {
				return err
			}
			plan := aptcheck.PlanNoSubscription(before)
			if plan.Empty() {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%s : déjà sans dépôt enterprise, avec no-subscription : rien à faire.\n", node)
				return output.Render(cmd.OutOrStdout(), opts, plan, noSubRows(node, plan, "déjà en place"))
			}

			result, err := newRunner(cmd, client).Run(cmd.Context(), service.Mutation{
				Target: node,
				Plan:   noSubServicePlan(node, plan),
				PreRead: func(context.Context) (service.State, error) {
					return service.State{Exists: true, Status: "à basculer", Raw: plan}, nil
				},
				Write: func(ctx context.Context) (string, error) {
					return "", applyNoSub(ctx, client, node, before.Digest, plan)
				},
				PostRead: func(ctx context.Context) (service.State, error) {
					after, err := client.AptRepositories(ctx, node)
					if err != nil {
						return service.State{}, err
					}
					if left := aptcheck.PlanNoSubscription(after); !left.Empty() {
						return service.State{}, fmt.Errorf(
							"les écritures ont été acceptées, mais la relecture montre encore : %d ajout(s), %d dépôt(s) enterprise actif(s).\n"+
								"  pvecli node repo ls %s", len(left.Add), len(left.Disable), node)
					}
					return service.State{Exists: true, Status: "basculé", Summary: repoVerdict(after), Raw: plan}, nil
				},
			})
			if err != nil {
				return err
			}
			if result.Status != "basculé" {
				// --dry-run: the runner stopped after the plan.
				return nil
			}
			return output.Render(cmd.OutOrStdout(), opts, plan, noSubRows(node, plan, "fait, relu sur le nœud"))
		},
	}
	addWriteFlags(c)
	addRenderFlags(c)
	return c
}

// applyNoSub sends the writes, additions first. The pre-read digest guards
// the first one; each later write re-reads to get the digest its predecessor
// produced, rather than going without.
func applyNoSub(ctx context.Context, client *pve.Client, node, digest string, plan aptcheck.NoSubPlan) error {
	first := true
	nextDigest := func() (string, error) {
		if first {
			first = false
			return digest, nil
		}
		r, err := client.AptRepositories(ctx, node)
		if err != nil {
			return "", err
		}
		return r.Digest, nil
	}

	for _, handle := range plan.Add {
		d, err := nextDigest()
		if err != nil {
			return err
		}
		if err := client.AddStandardRepo(ctx, node, handle, d); err != nil {
			return fmt.Errorf("ajout du dépôt « %s » : %w", handle, err)
		}
	}
	for _, ref := range plan.Disable {
		d, err := nextDigest()
		if err != nil {
			return err
		}
		if err := client.SetRepoEnabled(ctx, node, ref.Path, ref.Index, false, d); err != nil {
			return fmt.Errorf("désactivation de %s #%d : %w", ref.Path, ref.Index, err)
		}
	}
	return nil
}

func noSubServicePlan(node string, plan aptcheck.NoSubPlan) service.Plan {
	var effect, rollback []string
	for _, h := range plan.Add {
		effect = append(effect, "PUT handle="+h)
	}
	for _, r := range plan.Disable {
		effect = append(effect, fmt.Sprintf("POST path=%s index=%d enabled=0  (%s)", r.Path, r.Index, r.URI))
		rollback = append(rollback, fmt.Sprintf("pvesh create /nodes/%s/apt/repositories --path %s --index %d --enabled 1",
			node, r.Path, r.Index))
	}
	if len(rollback) == 0 {
		rollback = append(rollback, "aucun dépôt désactivé ; les ajouts se désactivent dans Updates > Repositories")
	}
	return service.Plan{
		Node:     node,
		Method:   "PUT puis POST",
		Path:     "/nodes/" + node + "/apt/repositories",
		Effect:   strings.Join(effect, "\n           "),
		Rollback: strings.Join(rollback, "\n           "),
		Verify:   "relecture des dépôts : plus aucun enterprise actif, no-subscription présent",
	}
}

func noSubRows(node string, plan aptcheck.NoSubPlan, status string) output.Rows {
	rows := output.Rows{Headers: []string{"CHAMP", "VALEUR"}, Cells: [][]string{{"nœud", node}, {"statut", status}}}
	for _, h := range plan.Add {
		rows.Cells = append(rows.Cells, []string{"ajouté", h})
	}
	for _, r := range plan.Disable {
		rows.Cells = append(rows.Cells, []string{"désactivé", fmt.Sprintf("%s #%d (%s)", r.Path, r.Index, r.URI)})
	}
	return rows
}

// nodeUpdatesSuccessTTL bounds how often the shell hook asks the node. The
// node refreshes its own package index once a day (pve-daily-update), so a
// fresher answer than a few hours would only repeat the same one.
const nodeUpdatesSuccessTTL = 6 * time.Hour

// nodeUpdatesFailureTTL is short for the same reason as updateCheckFailureTTL
// (cmd/update.go): a node that was off or unreachable must not keep the
// notification silent for the rest of the day once it is back.
const nodeUpdatesFailureTTL = 1 * time.Hour

// nodeUpdatesRefreshTimeout caps the background refresh. Nobody waits on it,
// but a process left hanging on an unreachable node is still a leak.
const nodeUpdatesRefreshTimeout = 20 * time.Second

const nodeUpdatesCacheSchema = 1

type nodeUpdatesCache struct {
	Schema    int       `json:"schema"`
	CheckedAt time.Time `json:"checked_at"`
	Success   bool      `json:"success"`
	Node      string    `json:"node"`
	// Line is the notification, already worded; empty when there is nothing
	// to say. Storing the sentence rather than the report keeps --notify a
	// file read and a print, nothing else.
	Line string `json:"line"`
}

func newNodeUpdatesCmd() *cobra.Command {
	var sync, notify, refresh, force, exitCode, all bool

	c := &cobra.Command{
		Use:     "updates [node]",
		Aliases: []string{"apt"},
		Short:   "Dit s'il faut mettre le nœud à jour (paquets, noyau, redémarrage)",
		Long: `Répond à « dois-je mettre à jour mon Proxmox ? » à partir de ce que le nœud sait :

  · les paquets dont une version plus récente est disponible (pve-manager en tête) ;
  · si cette mise à jour apporte un nouveau noyau ;
  · si un noyau plus récent que celui qui tourne est DÉJÀ installé : il faut alors
    redémarrer, mise à jour ou pas ;
  · si pveproxy/pvedaemon tournent encore sur une version antérieure de pve-manager.

LA FRAÎCHEUR. La réponse est celle de l'index APT du nœud, que PVE rafraîchit
lui-même une fois par jour (pve-daily-update.timer). --sync lance d'abord
« apt update » sur le nœud et attend la tâche : c'est la réponse à l'instant.

LES PRIVILÈGES. /apt/versions (paquets Proxmox importants, noyaux) ne demande
que Sys.Audit. La liste COMPLÈTE des mises à jour (/apt/update), Debian
comprise, exige Sys.Modify, comme --sync. Sans ce privilège, la commande ne
plante pas : elle répond sur les paquets Proxmox et le dit (« partiel »).

CE QUE LA COMMANDE NE FAIT PAS : installer. L'API n'expose pas « apt
full-upgrade ». L'installation est « pvecli node upgrade » (SSH, puis
« apt full-upgrade » sur le nœud, jamais « apt upgrade »).

POUR UN SCRIPT, --exit-code : 0 à jour, 10 mises à jour disponibles,
11 redémarrage requis (prioritaire). Les erreurs gardent leurs codes (1 à 5).

POUR LE SHELL, --notify et --refresh suivent le contrat de « pvecli update
check » : --notify ne lit que le cache (aucun réseau, une ligne ou rien,
toujours 0), --refresh met le cache à jour en arrière-plan si son TTL (6 h)
est dépassé, sans jamais rien imprimer. « pvecli update install-hook » câble
les deux à l'ouverture de chaque terminal.

Endpoints : GET  /api2/json/nodes/{node}/apt/versions
            GET  /api2/json/nodes/{node}/apt/update
            POST /api2/json/nodes/{node}/apt/update   (--sync)`,
		Args: usage(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if notify && (refresh || sync || exitCode) {
				return &exitError{code: pve.ExitUsage,
					msg: "--notify ne lit que le cache : il ne se combine ni avec --refresh, ni avec --sync, ni avec --exit-code"}
			}
			if force && !refresh {
				return &exitError{code: pve.ExitUsage, msg: "--force ne s'emploie qu'avec --refresh"}
			}
			if notify {
				runNodeUpdatesNotify(cmd, args)
				return nil
			}
			if refresh {
				runNodeUpdatesRefresh(cmd, args, force)
				return nil
			}
			return runNodeUpdates(cmd, args, sync, exitCode, all)
		},
	}

	c.Flags().BoolVar(&sync, "sync", false, "lance d'abord « apt update » sur le nœud (Sys.Modify)")
	c.Flags().BoolVar(&all, "all", false, "liste tous les paquets en attente, pas seulement les points saillants")
	c.Flags().BoolVar(&exitCode, "exit-code", false, "code de sortie 10 si mises à jour, 11 si redémarrage requis")
	c.Flags().BoolVar(&notify, "notify", false, "lecture seule du cache, jamais de réseau (pour un .zshrc)")
	c.Flags().BoolVar(&refresh, "refresh", false, "rafraîchit le cache si périmé, aucune sortie jamais (arrière-plan)")
	c.Flags().BoolVar(&force, "force", false, "avec --refresh : ignore le TTL du cache")
	addRenderFlags(c)
	return c
}

// assessNode reads what the node knows and builds the report. A 403 on the
// full list is not an error: the report says it is partial.
func assessNode(ctx context.Context, client *pve.Client, node string) (aptcheck.Report, error) {
	versions, err := client.AptVersions(ctx, node)
	if err != nil {
		return aptcheck.Report{}, err
	}
	updates, err := client.AptUpdates(ctx, node)
	full := err == nil
	if err != nil {
		var apiErr *pve.APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
			return aptcheck.Report{}, err
		}
	}
	return aptcheck.Assess(node, versions, updates, full), nil
}

func runNodeUpdates(cmd *cobra.Command, args []string, sync, exitCode, all bool) error {
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

	if sync {
		if err := syncAptIndex(cmd, client, node); err != nil {
			return err
		}
	}

	rep, err := assessNode(cmd.Context(), client, node)
	if err != nil {
		return err
	}
	writeNodeUpdatesCache(cmd, node, &nodeUpdatesCache{Success: true, Line: notifyLine(rep)})

	if err := output.Render(cmd.OutOrStdout(), opts, rep, updatesRows(rep, all)); err != nil {
		return err
	}
	if opts.Format == output.Table {
		if rep.Partial {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(),
				"partiel : sans Sys.Modify sur /nodes/"+node+", seuls les paquets Proxmox importants sont comparés (Debian exclu).")
		}
		if majors := rep.Majors(); len(majors) > 0 {
			m := majors[0]
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
				"attention : %d paquet(s) changent de version MAJEURE (%s %s → %s…).\n"+
					"  souvent un dépôt qui pointe sur la série suivante (Ceph : ceph.sources). Vérifie avant d'installer :\n"+
					"  pvecli node repo ls %s\n", len(majors), m.Package, m.Installed, m.Candidate, rep.Node)
		}
		if rep.UpdatesAvailable() || rep.RebootRequired() || rep.RestartDaemons {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "\n"+updatesAdvice(rep))
		}
	}

	if exitCode {
		// The verdict is already on stdout: a second « Error: » line from
		// Cobra would present a node that needs care as a failed command.
		cmd.SilenceErrors = true
		switch {
		case rep.RebootRequired():
			return &exitError{code: exitRebootRequired, msg: "redémarrage requis"}
		case rep.UpdatesAvailable():
			return &exitError{code: exitUpdatesAvailable, msg: "mises à jour disponibles"}
		}
	}
	return nil
}

// syncAptIndex runs « apt update » on the node and follows the task to its end.
func syncAptIndex(cmd *cobra.Command, client *pve.Client, node string) error {
	raw, err := client.RefreshAptIndex(cmd.Context(), node)
	if err != nil {
		return err
	}
	upid, err := pve.ParseUPID(raw)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "apt update sur %s : tâche %s\n", node, upid)
	waiter := &service.TaskWaiter{API: client, Progress: cmd.ErrOrStderr(), Quiet: !output.IsTerminal(cmd.ErrOrStderr())}
	task, err := waiter.Wait(cmd.Context(), upid)
	if err != nil {
		return err
	}
	if !task.Succeeded() {
		msg := "apt update a échoué sur " + node + " : " + task.ExitStatus
		if lines, err := waiter.Tail(cmd.Context(), upid, 20); err == nil && len(lines) > 0 {
			msg += "\n\n" + strings.Join(lines, "\n")
		}
		msg += "\n\n  un dépôt enterprise sans abonnement échoue ainsi : pvecli node repo ls " + node
		return &service.TaskError{UPID: upid.String(), Msg: msg}
	}
	return nil
}

func updatesRows(rep aptcheck.Report, all bool) output.Rows {
	rows := output.Rows{Headers: []string{"CHAMP", "VALEUR"}, Cells: [][]string{
		{"nœud", rep.Node},
		{"verdict", rep.Verdict()},
	}}
	if rep.Manager.Installed != "" {
		v := rep.Manager.Installed
		if rep.Manager.Candidate != rep.Manager.Installed {
			v += " → " + rep.Manager.Candidate
		}
		rows.Cells = append(rows.Cells, []string{"pve-manager", v})
	}
	kernel := rep.RunningKernel
	if rep.NewestKernel != "" && rep.NewestKernel != rep.RunningKernel {
		kernel += " (installé : " + rep.NewestKernel + ")"
	}
	if rep.KernelUpdate {
		kernel += ", nouveau noyau dans la mise à jour"
	}
	rows.Cells = append(rows.Cells, []string{"noyau", kernel})
	if rep.RestartDaemons {
		rows.Cells = append(rows.Cells, []string{"services", "pveproxy/pvedaemon tournent sur une version antérieure"})
	}

	proxmox, others := 0, 0
	for _, p := range rep.Pending {
		if p.Origin == "Proxmox" {
			proxmox++
		} else {
			others++
		}
	}
	scope := ""
	if rep.Partial {
		scope = " (partiel)"
	}
	rows.Cells = append(rows.Cells, []string{"en attente", fmt.Sprintf("%d Proxmox, %d autres%s", proxmox, others, scope)})

	shown := 0
	for _, p := range rep.Pending {
		if !all && (p.Origin != "Proxmox" || shown >= 10) {
			continue
		}
		shown++
		installed := p.Installed
		if installed == "" {
			installed = "(nouveau)"
		}
		rows.Cells = append(rows.Cells, []string{"  " + p.Package, installed + " → " + p.Candidate})
	}
	if hidden := len(rep.Pending) - shown; hidden > 0 {
		rows.Cells = append(rows.Cells, []string{"", fmt.Sprintf("… %d de plus : --all", hidden)})
	}
	return rows
}

func updatesAdvice(rep aptcheck.Report) string {
	var b strings.Builder
	b.WriteString("à faire :\n")
	if rep.UpdatesAvailable() {
		b.WriteString("  pvecli node upgrade\n")
	}
	if rep.RebootRequired() || rep.KernelUpdate {
		b.WriteString("  puis : pvecli node reboot " + rep.Node + "   (vérifie onboot=1 sur les invités avant)\n")
	} else if rep.RestartDaemons {
		b.WriteString("  systemctl restart pveproxy pvedaemon\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// notifyLine is the sentence the shell hook prints, or "" when the node needs
// nothing.
func notifyLine(rep aptcheck.Report) string {
	if !rep.UpdatesAvailable() && !rep.RebootRequired() {
		return ""
	}
	head := fmt.Sprintf("nœud %s : %d mise(s) à jour", rep.Node, len(rep.Pending))
	if !rep.UpdatesAvailable() {
		head = "nœud " + rep.Node + " : redémarrage requis"
	}
	if h := rep.Highlights(); len(h) > 0 && rep.UpdatesAvailable() {
		head += " (" + strings.Join(h, ", ") + ")"
	}
	return head + " ; détail : pvecli node updates\n"
}

func runNodeUpdatesNotify(cmd *cobra.Command, args []string) {
	node, err := targetNode(cmd, args)
	if err != nil {
		return
	}
	c, err := readNodeUpdatesCache(nodeUpdatesCachePath(cmd, node))
	if err != nil || !c.Success || time.Since(c.CheckedAt) >= nodeUpdatesSuccessTTL+nodeUpdatesFailureTTL {
		// A stale answer is not shown: a reboot done since then would keep
		// being announced. One TTL of grace covers a refresh that is late.
		return
	}
	_, _ = fmt.Fprint(cmd.OutOrStdout(), c.Line)
}

func runNodeUpdatesRefresh(cmd *cobra.Command, args []string, force bool) {
	node, err := targetNode(cmd, args)
	if err != nil {
		return
	}
	path := nodeUpdatesCachePath(cmd, node)
	if !force {
		if c, err := readNodeUpdatesCache(path); err == nil {
			ttl := nodeUpdatesFailureTTL
			if c.Success {
				ttl = nodeUpdatesSuccessTTL
			}
			if time.Since(c.CheckedAt) < ttl {
				return
			}
		}
	}
	client, err := newClient(cmd)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), nodeUpdatesRefreshTimeout)
	defer cancel()
	rep, err := assessNode(ctx, client, node)
	if err != nil {
		writeNodeUpdatesCache(cmd, node, &nodeUpdatesCache{Success: false})
		return
	}
	writeNodeUpdatesCache(cmd, node, &nodeUpdatesCache{Success: true, Line: notifyLine(rep)})
}

var cacheKeyUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// nodeUpdatesCachePath keys the cache by context AND node: two contexts may
// both have a node called « pve », and they are not the same machine.
func nodeUpdatesCachePath(cmd *cobra.Command, node string) string {
	ctxName := "default"
	if eff, err := resolveConfig(cmd); err == nil && eff.ContextName != "" {
		ctxName = eff.ContextName
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	name := cacheKeyUnsafe.ReplaceAllString(ctxName+"-"+node, "_")
	return filepath.Join(dir, "pvecli", "node-updates-"+name+".json")
}

func readNodeUpdatesCache(path string) (*nodeUpdatesCache, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c nodeUpdatesCache
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	if c.Schema != nodeUpdatesCacheSchema {
		return nil, fmt.Errorf("cache dans un format obsolète")
	}
	return &c, nil
}

func writeNodeUpdatesCache(cmd *cobra.Command, node string, c *nodeUpdatesCache) {
	c.Schema = nodeUpdatesCacheSchema
	c.CheckedAt = time.Now()
	c.Node = node
	path := nodeUpdatesCachePath(cmd, node)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, raw, 0o600)
}
