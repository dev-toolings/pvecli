package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dev-toolings/pvecli/internal/output"
	"github.com/dev-toolings/pvecli/internal/pve"
	"github.com/dev-toolings/pvecli/internal/service"
)

// newNotifyCmd pilote le sous-système de notification du cluster.
//
// C'est la famille qui ferme la boucle des autres. « backup job ls » dit qu'un
// job est planifié, « dr drill » dit qu'une archive se restaure. Ni l'un ni
// l'autre ne dit qui apprend l'échec, à 3 h du matin, quand personne ne regarde.
func newNotifyCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "notify",
		Short: "Notifications : cibles, routage, envoi de test",
		Long: `Gère les notifications du cluster (/cluster/notifications).

Un nœud fraîchement installé a UNE cible, « mail-to-root », et un matcher qui
lui envoie tout. Sur un lab sans serveur de mail sortant, cela veut dire que
l'échec d'une sauvegarde est notifié dans une boîte locale que personne n'ouvre.
Le job est surveillé sur le papier, et silencieux en pratique.

Trois notions, et l'ordre compte :

  1. Une CIBLE reçoit (webhook, gotify, smtp, sendmail).
  2. Un MATCHER route vers une cible. Sans lui, la cible ne reçoit RIEN, et c'est
     l'erreur qui donne l'impression d'avoir posé une alerte.
  3. Un ENVOI DE TEST est la seule chose qui prouve que la chaîne existe. Une
     alerte jamais testée et une alerte cassée ont exactement la même tête :
     dans les deux cas, il ne se passe rien.

  pvecli notify webhook create discord --discord "$DISCORD_WEBHOOK"
  pvecli notify matcher create discord-alerts --target discord --severity warning,error
  pvecli notify target test discord

Privilèges : Sys.Audit sur / pour lire, Sys.Modify sur / pour écrire. Attention,
c'est bien « / » et non « /nodes/{node} ». Un token qui administre le nœud
sans porter Sys.Modify à la racine repart avec un 403 sur toute écriture ici.`,
		Args: usage(cobra.NoArgs),
	}
	c.AddCommand(newNotifyTargetCmd(), newNotifyWebhookCmd(), newNotifyMatcherCmd())
	return c
}

// ---------------------------------------------------------------- target

func newNotifyTargetCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "target",
		Short: "Cibles de notification : lister, tester",
		Long: `Vue unifiée des cibles, tous types confondus.

PVE écrit les cibles par type (…/endpoints/webhook, …/endpoints/smtp) mais les
expose ensemble ici. C'est la vue qui répond à « qu'est-ce qui est branché sur
ce nœud », donc la première à consulter.`,
		Args: usage(cobra.NoArgs),
	}
	c.AddCommand(newNotifyTargetListCmd(), newNotifyTargetTestCmd())
	return c
}

func newNotifyTargetListCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "Liste les cibles de notification (GET /cluster/notifications/targets)",
		Long: `Liste toutes les cibles déclarées.

Une cible « builtin » est celle que PVE pose à l'installation : mail-to-root,
qui écrit dans la boîte locale de root@pam. Elle est présente sur tous les
nœuds, et sur un lab sans MTA sortant elle ne notifie personne.

Endpoint : GET /api2/json/cluster/notifications/targets`,
		Args: usage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := renderOptions(cmd)
			if err != nil {
				return err
			}
			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			targets, err := client.NotifyTargets(cmd.Context())
			if err != nil {
				return err
			}

			onlyBuiltin := true
			rows := output.Rows{Headers: []string{"NOM", "TYPE", "ORIGINE", "ÉTAT", "COMMENTAIRE"}}
			for _, t := range targets {
				if !t.Builtin() {
					onlyBuiltin = false
				}
				rows.Cells = append(rows.Cells, []string{
					t.Name, t.Type, t.Origin, targetState(t), orDash(t.Comment),
				})
			}
			if onlyBuiltin {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(),
					"⚠ aucune cible ajoutée : seule « mail-to-root » existe, et elle poste dans la boîte\n"+
						"  locale de root@pam. Sans MTA sortant, l'échec d'une sauvegarde n'atteint personne.\n"+
						"  pvecli notify webhook create discord --discord \"$DISCORD_WEBHOOK\"")
			}
			return output.Render(cmd.OutOrStdout(), opts, targets, rows)
		},
	}
	addRenderFlags(c)
	return c
}

func targetState(t pve.NotifyTarget) string {
	if t.IsEnabled() {
		return "actif"
	}
	return "désactivé"
}

func newNotifyTargetTestCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "test <nom>",
		Short: "Envoie une notification de test (POST /cluster/notifications/targets/{name}/test)",
		Long: `Envoie une vraie notification à une cible.

C'est la seule commande de cette famille qui prouve quelque chose. Tout le
reste décrit une configuration ; celle-ci fait partir un message.

Et elle ne prouve qu'une moitié : un retour sans erreur veut dire que le nœud a
accepté d'essayer, pas que le message est arrivé. La preuve finale est dans le
salon Discord ou la boîte mail, jamais dans ce terminal.

Endpoint : POST /api2/json/cluster/notifications/targets/{name}/test (Sys.Modify sur /)`,
		Args: usage(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			runner := newRunner(cmd, client)

			_, err = runner.Run(cmd.Context(), service.Mutation{
				Target: name,
				Plan: service.Plan{
					Method:   "POST",
					Path:     pve.NotifyTargetTestPath(name),
					Effect:   fmt.Sprintf("envoie une notification de test à « %s »", name),
					Rollback: "aucun : un message envoyé ne se rappelle pas",
					Verify:   "le message doit APPARAÎTRE côté destinataire ; ce retour ne le prouve pas",
				},
				PreRead: func(ctx context.Context) (service.State, error) {
					targets, err := client.NotifyTargets(ctx)
					if err != nil {
						return service.State{}, err
					}
					for _, t := range targets {
						if t.Name != name {
							continue
						}
						if !t.IsEnabled() {
							return service.State{}, fmt.Errorf(
								"la cible %q est désactivée, un test ne partirait pas", name)
						}
						return service.State{Exists: true, Status: "cible " + t.Type, Raw: t}, nil
					}
					return service.State{}, fmt.Errorf(
						"aucune cible %q :\n  pvecli notify target ls", name)
				},
				Write: func(ctx context.Context) (string, error) {
					return "", client.TestNotifyTarget(ctx, name)
				},
				// Rien à relire : un envoi ne laisse aucune trace dans la
				// configuration. Rendre l'état d'avant serait honnête sur le
				// plan technique et trompeur à l'écran, donc le PostRead dit
				// ce qu'il sait : l'envoi est parti, la réception est ailleurs.
				PostRead: func(ctx context.Context) (service.State, error) {
					return service.State{
						Exists:  true,
						Status:  "envoyé",
						Summary: "envoi accepté par le nœud",
					}, nil
				},
			})
			if err != nil {
				return err
			}
			if dryRun, _ := cmd.Flags().GetBool("dry-run"); dryRun {
				return nil
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
				"\nle nœud a accepté l'envoi vers « %s ». Va VÉRIFIER que le message est arrivé :\n"+
					"  une cible qui accepte et ne délivre pas est exactement ce que ce test doit attraper.\n", name)
			return nil
		},
	}
	addWriteFlags(c)
	return c
}

// ---------------------------------------------------------------- webhook

func newNotifyWebhookCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "webhook",
		Short: "Cibles webhook : lister, décrire, créer, supprimer",
		Long: `Gère les cibles de type webhook (/cluster/notifications/endpoints/webhook).

Un webhook poste un corps HTTP que tu contrôles vers une URL que tu contrôles.
C'est le seul type de cible qui ne dépend d'aucune infrastructure à maintenir :
ni MTA, ni serveur Gotify.

Les SECRETS ne ressortent jamais. Le nœud rend « name=token » sans sa valeur,
ce qui rend une cible sûre à lister, et c'est la raison de porter la partie
sensible d'une URL dans un secret plutôt qu'en clair dans le champ URL.`,
		Args: usage(cobra.NoArgs),
	}
	c.AddCommand(
		newNotifyWebhookListCmd(), newNotifyWebhookShowCmd(),
		newNotifyWebhookCreateCmd(), newNotifyWebhookRmCmd(),
	)
	return c
}

func newNotifyWebhookListCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "Liste les cibles webhook (GET /cluster/notifications/endpoints/webhook)",
		Long: `Liste les webhooks déclarés.

La colonne SECRETS ne montre que les NOMS : le nœud ne rend pas les valeurs.

Endpoint : GET /api2/json/cluster/notifications/endpoints/webhook`,
		Args: usage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := renderOptions(cmd)
			if err != nil {
				return err
			}
			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			hooks, err := client.NotifyWebhooks(cmd.Context())
			if err != nil {
				return err
			}
			rows := output.Rows{Headers: []string{"NOM", "MÉTHODE", "URL", "SECRETS", "ÉTAT", "COMMENTAIRE"}}
			for _, w := range hooks {
				state := "actif"
				if !w.IsEnabled() {
					state = "désactivé"
				}
				rows.Cells = append(rows.Cells, []string{
					w.Name, strings.ToUpper(w.Method), w.URL,
					orDash(strings.Join(w.SecretNames(), ",")), state, orDash(w.Comment),
				})
			}
			return output.Render(cmd.OutOrStdout(), opts, hooks, rows)
		},
	}
	addRenderFlags(c)
	return c
}

func newNotifyWebhookShowCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "show <nom>",
		Short: "Décrit une cible webhook (GET /cluster/notifications/endpoints/webhook/{name})",
		Long: `Affiche la définition complète d'un webhook, gabarit de corps compris.

Le corps est stocké en base64 par PVE, pour qu'un JSON à accolades survive au
format de configuration à property strings. Il est rendu ici EN CLAIR : un
gabarit qu'on ne peut pas relire est un gabarit qu'on ne peut pas corriger.

Endpoint : GET /api2/json/cluster/notifications/endpoints/webhook/{name}`,
		Args: usage(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := renderOptions(cmd)
			if err != nil {
				return err
			}
			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			w, err := client.NotifyWebhookByName(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			state := "actif"
			if !w.IsEnabled() {
				state = "désactivé"
			}
			rows := output.Rows{Headers: []string{"CHAMP", "VALEUR"}, Cells: [][]string{
				{"nom", w.Name},
				{"méthode", strings.ToUpper(w.Method)},
				{"url", w.URL},
				{"secrets", orDash(strings.Join(w.SecretNames(), ", "))},
				{"corps", orDash(w.DecodedBody())},
				{"état", state},
				{"origine", orDash(w.Origin)},
				{"commentaire", orDash(w.Comment)},
			}}
			return output.Render(cmd.OutOrStdout(), opts, w, rows)
		},
	}
	addRenderFlags(c)
	return c
}

func newNotifyWebhookCreateCmd() *cobra.Command {
	var (
		discord string
		hookURL string
		method  string
		body    string
		comment string
		headers []string
		secrets []string
	)

	c := &cobra.Command{
		Use:   "create <nom>",
		Short: "Crée une cible webhook (POST /cluster/notifications/endpoints/webhook)",
		Long: `Crée une cible webhook.

DEUX MODES. Le premier est un raccourci, le second est le mode général.

  --discord <url>   monte une cible Discord complète à partir de l'URL que
                    Discord fournit. Rien d'autre à passer.
  --url … --body …  monte n'importe quel webhook, à la main.

Le raccourci Discord existe parce que le montage manuel tombe dans deux pièges,
et que les deux rendent une erreur qui ne les nomme pas :

  1. Le champ « url » est validé contre une regex d'URL par le nœud. Y écrire un
     gabarit entier, « {{ secrets.url }} », est refusé par un « value does not
     match the regex pattern » qui ne dit pas que le gabarit est le problème.
     Le raccourci coupe donc l'URL : la partie stable reste en clair, l'id et le
     jeton partent dans deux secrets distincts.
  2. Discord n'accepte QUE du JSON et rejette une requête sans en-tête
     « Content-Type: application/json ». Sans corps déclaré, PVE poste son rendu
     texte, Discord répond 400, et RIEN n'apparaît dans le salon, l'échec le
     plus silencieux de la chaîne.

Le gabarit passe le titre et le message par le filtre « escape » du moteur de
rendu. Sans lui, un message contenant un guillemet fabriquerait un JSON invalide
et l'alerte disparaîtrait le jour précis où elle avait quelque chose à dire.

CRÉER UNE CIBLE NE SUFFIT PAS. Tant qu'aucun matcher ne la route, elle ne reçoit
rien :

  pvecli notify matcher create discord-alerts --target <nom> --severity warning,error

L'URL n'est jamais affichée : ni dans le plan de --dry-run, ni dans une erreur.

Endpoint : POST /api2/json/cluster/notifications/endpoints/webhook (Sys.Modify sur /)`,
		Args: usage(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			opts, err := renderOptions(cmd)
			if err != nil {
				return err
			}

			var wo pve.WebhookOptions
			switch {
			case discord != "" && hookURL != "":
				return &exitError{code: pve.ExitUsage,
					msg: "--discord et --url s'excluent : le premier fabrique le second"}
			case discord != "":
				wo, err = pve.DiscordWebhook(name, discord, comment)
				if err != nil {
					return &exitError{code: pve.ExitUsage, msg: err.Error()}
				}
			case hookURL != "":
				wo = pve.WebhookOptions{
					Name: name, URL: hookURL, Method: method, Body: body, Comment: comment,
				}
				if wo.Headers, err = parseKeyValues(headers, "--header"); err != nil {
					return err
				}
				if wo.Secrets, err = parseKeyValues(secrets, "--secret"); err != nil {
					return err
				}
			default:
				return &exitError{code: pve.ExitUsage,
					msg: "il faut --discord <url> ou --url <url>"}
			}

			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			runner := newRunner(cmd, client)

			result, err := runner.Run(cmd.Context(), service.Mutation{
				Target: name,
				Plan: service.Plan{
					Method: "POST",
					Path:   pve.NotifyWebhooksPath(),
					// Le payload porte l'URL et les secrets. Il n'est PAS
					// passé au plan : un --dry-run est fait pour être copié
					// dans un ticket, et il emporterait le jeton avec lui.
					Effect:   fmt.Sprintf("cible webhook « %s » vers %s", name, redactedHost(wo.URL)),
					Rollback: "pvecli notify webhook rm " + name,
					Verify:   "pvecli notify target test " + name + " (puis vérifier côté destinataire)",
				},
				PreRead: func(ctx context.Context) (service.State, error) {
					hooks, err := client.NotifyWebhooks(ctx)
					if err != nil {
						return service.State{}, err
					}
					for _, w := range hooks {
						if w.Name == name {
							return service.State{}, fmt.Errorf(
								"la cible %q existe déjà, supprime-la ou choisis un autre nom :\n"+
									"  pvecli notify webhook show %s", name, name)
						}
					}
					return service.State{Exists: true, Status: "nom libre"}, nil
				},
				Write: func(ctx context.Context) (string, error) {
					return "", client.CreateNotifyWebhook(ctx, wo)
				},
				PostRead: func(ctx context.Context) (service.State, error) {
					w, err := client.NotifyWebhookByName(ctx, name)
					if err != nil {
						return service.State{}, err
					}
					return service.State{Exists: true, Status: "créée", Raw: *w}, nil
				},
			})
			if err != nil {
				return err
			}
			if dryRun, _ := cmd.Flags().GetBool("dry-run"); dryRun {
				return nil
			}

			// Une cible sans matcher est une cible muette. Le dire ici, au
			// moment où elle vient d'être créée, est la seule fenêtre où
			// l'information sert encore.
			if err := warnIfUnrouted(cmd, client, name); err != nil {
				return err
			}

			w, ok := result.Raw.(pve.NotifyWebhook)
			if !ok {
				return nil
			}
			rows := output.Rows{Headers: []string{"CHAMP", "VALEUR"}, Cells: [][]string{
				{"nom", w.Name},
				{"méthode", strings.ToUpper(w.Method)},
				{"url", w.URL},
				{"secrets", orDash(strings.Join(w.SecretNames(), ", "))},
				{"corps", orDash(w.DecodedBody())},
			}}
			return output.Render(cmd.OutOrStdout(), opts, w, rows)
		},
	}

	f := c.Flags()
	f.StringVar(&discord, "discord", "", "URL de webhook Discord : monte la cible complète (en-tête, corps JSON, secrets)")
	f.StringVar(&hookURL, "url", "", "URL cible, gabarits « {{ secrets.x }} » acceptés hors du schéma et de l'hôte")
	f.StringVar(&method, "method", "post", "méthode HTTP : post, put ou get")
	f.StringVar(&body, "body", "", "gabarit du corps ; « {{ escape message }} » échappe pour du JSON")
	f.StringVar(&comment, "comment", "", "description de la cible")
	f.StringArrayVar(&headers, "header", nil, "en-tête HTTP « Nom: valeur » (répétable)")
	f.StringArrayVar(&secrets, "secret", nil, "secret « nom=valeur » (répétable), jamais rendu par le nœud")
	addWriteFlags(c)
	addRenderFlags(c)
	return c
}

// redactedHost ne garde que l'hôte d'une URL. Un plan doit dire où part la
// requête sans emporter le jeton qui l'autorise.
func redactedHost(raw string) string {
	rest := raw
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	if rest == "" {
		return "<url>"
	}
	return rest
}

// parseKeyValues lit les drapeaux répétables « nom=valeur » et « Nom: valeur ».
func parseKeyValues(items []string, flag string) (map[string]string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(items))
	for _, item := range items {
		sep := strings.IndexAny(item, "=:")
		if sep <= 0 {
			return nil, &exitError{code: pve.ExitUsage,
				msg: fmt.Sprintf("%s %q : forme attendue « nom=valeur »", flag, item)}
		}
		key := strings.TrimSpace(item[:sep])
		value := strings.TrimSpace(item[sep+1:])
		if value == "" {
			return nil, &exitError{code: pve.ExitUsage,
				msg: fmt.Sprintf("%s %q : valeur vide", flag, item)}
		}
		out[key] = value
	}
	return out, nil
}

// warnIfUnrouted prévient qu'aucun matcher ne route vers la cible. L'erreur est
// silencieuse par nature : la cible existe, elle est correcte, et elle ne reçoit
// rien.
func warnIfUnrouted(cmd *cobra.Command, client *pve.Client, target string) error {
	matchers, err := client.NotifyMatchers(cmd.Context())
	if err != nil {
		// Ne pas faire échouer une création réussie sur un avertissement
		// qu'on n'a pas pu calculer.
		return nil
	}
	for _, m := range matchers {
		if !m.IsEnabled() {
			continue
		}
		for _, t := range m.Target {
			if t == target {
				return nil
			}
		}
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
		"\n⚠ aucun matcher actif ne route vers « %s » : la cible existe et ne recevra RIEN.\n"+
			"  pvecli notify matcher create %s-alerts --target %s --severity warning,error\n",
		target, target, target)
	return nil
}

func newNotifyWebhookRmCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "rm <nom>",
		Aliases: []string{"delete"},
		Short:   "Supprime une cible webhook (DELETE /cluster/notifications/endpoints/webhook/{name})",
		Long: `Supprime une cible webhook.

Le nœud REFUSE tant qu'un matcher la référence. C'est une bonne nouvelle :
l'ordre de démontage est imposé plutôt que deviné, et on ne se retrouve pas avec
un matcher qui route vers le vide.

Endpoint : DELETE /api2/json/cluster/notifications/endpoints/webhook/{name} (Sys.Modify sur /)`,
		Args: usage(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			runner := newRunner(cmd, client)

			_, err = runner.Run(cmd.Context(), service.Mutation{
				Target:      name,
				Destructive: true,
				Plan: service.Plan{
					Method:   "DELETE",
					Path:     pve.NotifyWebhookPath(name),
					Effect:   fmt.Sprintf("supprime la cible « %s » : plus aucune notification n'y partira", name),
					Rollback: "aucun : les secrets ne sont pas récupérables, il faudra les ressaisir",
					Verify:   "la cible doit disparaître de /cluster/notifications/targets",
				},
				PreRead: func(ctx context.Context) (service.State, error) {
					w, err := client.NotifyWebhookByName(ctx, name)
					if err != nil {
						return service.State{}, err
					}
					return service.State{Exists: true, Status: "présente", Raw: *w}, nil
				},
				Write: func(ctx context.Context) (string, error) {
					return "", client.DeleteNotifyWebhook(ctx, name)
				},
				PostRead: func(ctx context.Context) (service.State, error) {
					hooks, err := client.NotifyWebhooks(ctx)
					if err != nil {
						return service.State{}, err
					}
					for _, w := range hooks {
						if w.Name == name {
							return service.State{}, fmt.Errorf(
								"la cible %q est toujours là, la suppression n'a rien fait", name)
						}
					}
					return service.State{Exists: true, Status: "supprimée"}, nil
				},
			})
			return err
		},
	}
	addWriteFlags(c)
	return c
}

// ---------------------------------------------------------------- matcher

func newNotifyMatcherCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "matcher",
		Short: "Routage : lister, créer, supprimer",
		Long: `Gère le routage des notifications (/cluster/notifications/matchers).

Un matcher est ce qui relie un événement à une cible. Sans lui, une cible
parfaitement configurée ne reçoit rien.

Le matcher intégré « default-matcher » envoie TOUT vers mail-to-root. Il ne
s'occupe pas des cibles ajoutées ensuite : ajouter un webhook ne le modifie pas,
il faut son propre matcher.`,
		Args: usage(cobra.NoArgs),
	}
	c.AddCommand(newNotifyMatcherListCmd(), newNotifyMatcherCreateCmd(), newNotifyMatcherRmCmd())
	return c
}

func newNotifyMatcherListCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "Liste les matchers (GET /cluster/notifications/matchers)",
		Long: `Liste les règles de routage.

La colonne CRITÈRE dit ce que le matcher filtre. « tout » n'est pas un vide :
c'est un matcher sans aucun critère, qui prend donc chaque notification.

Endpoint : GET /api2/json/cluster/notifications/matchers`,
		Args: usage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := renderOptions(cmd)
			if err != nil {
				return err
			}
			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			matchers, err := client.NotifyMatchers(cmd.Context())
			if err != nil {
				return err
			}
			rows := output.Rows{Headers: []string{"NOM", "CIBLES", "CRITÈRE", "MODE", "ÉTAT", "COMMENTAIRE"}}
			for _, m := range matchers {
				state := "actif"
				if !m.IsEnabled() {
					state = "désactivé"
				}
				rows.Cells = append(rows.Cells, []string{
					m.Name, orDash(strings.Join(m.Target, ",")), m.Criteria(),
					orDash(m.Mode), state, orDash(m.Comment),
				})
			}
			return output.Render(cmd.OutOrStdout(), opts, matchers, rows)
		},
	}
	addRenderFlags(c)
	return c
}

func newNotifyMatcherCreateCmd() *cobra.Command {
	var (
		targets    []string
		severities []string
		fields     []string
		calendars  []string
		mode       string
		invert     bool
		comment    string
	)

	c := &cobra.Command{
		Use:   "create <nom>",
		Short: "Crée une règle de routage (POST /cluster/notifications/matchers)",
		Long: `Crée un matcher.

  pvecli notify matcher create discord-alerts --target discord --severity warning,error

UN MATCHER SANS CRITÈRE PREND TOUT. C'est légitime, et c'est rarement ce qu'on
veut sur un webhook : le succès de chaque sauvegarde nocturne finirait dans le
salon, et un canal qui parle tous les jours pour ne rien dire est un canal qu'on
finit par couper, juste avant la nuit où il avait raison.

« MODE ALL » ET PLUSIEURS SÉVÉRITÉS NE VONT PAS ENSEMBLE. « all » exige que
tous les critères tiennent EN MÊME TEMPS, y compris les entrées d'une même
liste. Or une notification porte une seule sévérité : demander warning ET error
donne une règle que le nœud accepte, affiche, et qui n'alerte jamais. C'est
mesuré, pas déduit : un vzdump en échec ne partait que vers mail-to-root avec
cette configuration. Cette commande bascule donc sur « any » d'elle-même dès
qu'il y a plusieurs sévérités, et refuse un « --mode all » explicite.

Les sévérités connues : info, notice, warning, error, unknown. « unknown » n'est
pas du bruit : c'est ce que porte un événement dont PVE n'a pas classé la
gravité, et l'exclure crée un angle mort.

Endpoint : POST /api2/json/cluster/notifications/matchers (Sys.Modify sur /)`,
		Args: usage(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			opts, err := renderOptions(cmd)
			if err != nil {
				return err
			}
			if len(targets) == 0 {
				return &exitError{code: pve.ExitUsage,
					msg: "il faut au moins un --target : un matcher sans cible ne route vers rien"}
			}
			// Les listes arrivent tantôt répétées, tantôt en une chaîne à
			// virgules. Elles repartent toujours en clés répétées : le nœud
			// accepte « warning,error » comme UNE valeur, et le matcher ne
			// correspond alors à aucune notification.
			severities = splitCommas(severities)
			for _, s := range severities {
				if !contains(pve.NotifySeverities, s) {
					return &exitError{code: pve.ExitUsage, msg: fmt.Sprintf(
						"sévérité %q inconnue, attendu : %s", s, strings.Join(pve.NotifySeverities, ", "))}
				}
			}

			// « mode all » exige que TOUS les critères tiennent en même temps,
			// et il applique cette règle aux entrées d'une même liste. Une
			// notification porte UNE sévérité : demander à la fois warning et
			// error donne donc une règle qui ne matche jamais. Le nœud
			// l'accepte, l'affiche, et n'alerte pas. Vérifié en direct sur le
			// lab le 18-08-2026, un vzdump en échec ne partait que vers
			// mail-to-root.
			//
			// Le défaut du schéma PVE est « all ». On ne le reprend pas tel
			// quel : quand l'opérateur n'a rien demandé, plusieurs sévérités
			// veulent dire « l'une d'elles », donc « any ». Quand il a demandé
			// « all » explicitement, on refuse plutôt que de le contredire en
			// silence.
			if len(severities) > 1 {
				if !cmd.Flags().Changed("mode") {
					mode = "any"
				} else if mode == "all" {
					return &exitError{code: pve.ExitUsage, msg: fmt.Sprintf(
						"--mode all avec %d sévérités donne une règle qui ne matche JAMAIS :\n"+
							"  une notification porte une seule sévérité, et « all » les exige toutes à la fois.\n"+
							"  utilise --mode any, ou une seule --severity", len(severities))}
				}
			}

			mo := pve.MatcherOptions{
				Name:       name,
				Targets:    splitCommas(targets),
				Severities: severities,
				Fields:     fields,
				Calendars:  calendars,
				Mode:       mode,
				Invert:     invert,
				Comment:    comment,
			}

			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			runner := newRunner(cmd, client)

			result, err := runner.Run(cmd.Context(), service.Mutation{
				Target: name,
				Plan: service.Plan{
					Method:   "POST",
					Path:     pve.NotifyMatchersPath(),
					Payload:  mo.Values(),
					Effect:   fmt.Sprintf("route %s vers %s", matcherCriteriaLabel(mo), strings.Join(mo.Targets, ", ")),
					Rollback: "pvecli notify matcher rm " + name,
					Verify:   "pvecli notify target test " + mo.Targets[0],
				},
				PreRead: func(ctx context.Context) (service.State, error) {
					matchers, err := client.NotifyMatchers(ctx)
					if err != nil {
						return service.State{}, err
					}
					for _, m := range matchers {
						if m.Name == name {
							return service.State{}, fmt.Errorf(
								"le matcher %q existe déjà :\n  pvecli notify matcher ls", name)
						}
					}
					// Une cible absente rend un matcher qui route vers le
					// vide, et le nœud l'accepte sans broncher.
					known, err := client.NotifyTargets(ctx)
					if err != nil {
						return service.State{}, err
					}
					for _, want := range mo.Targets {
						found := false
						for _, t := range known {
							if t.Name == want {
								found = true
								break
							}
						}
						if !found {
							return service.State{}, fmt.Errorf(
								"aucune cible %q, le matcher routerait vers le vide :\n  pvecli notify target ls", want)
						}
					}
					return service.State{Exists: true, Status: "nom libre"}, nil
				},
				Write: func(ctx context.Context) (string, error) {
					return "", client.CreateNotifyMatcher(ctx, mo)
				},
				PostRead: func(ctx context.Context) (service.State, error) {
					m, err := client.NotifyMatcherByName(ctx, name)
					if err != nil {
						return service.State{}, err
					}
					return service.State{Exists: true, Status: "créé", Raw: *m}, nil
				},
			})
			if err != nil {
				return err
			}
			if dryRun, _ := cmd.Flags().GetBool("dry-run"); dryRun {
				return nil
			}
			m, ok := result.Raw.(pve.NotifyMatcher)
			if !ok {
				return nil
			}
			if len(severities) == 0 && len(fields) == 0 && len(calendars) == 0 {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
					"\n⚠ ce matcher n'a AUCUN critère : il prend chaque notification, succès compris.\n"+
						"  pvecli notify matcher rm %s puis recrée-le avec --severity warning,error\n", name)
			}
			rows := output.Rows{Headers: []string{"CHAMP", "VALEUR"}, Cells: [][]string{
				{"nom", m.Name},
				{"cibles", orDash(strings.Join(m.Target, ", "))},
				{"critère", m.Criteria()},
				{"mode", orDash(m.Mode)},
			}}
			return output.Render(cmd.OutOrStdout(), opts, m, rows)
		},
	}

	f := c.Flags()
	f.StringArrayVar(&targets, "target", nil, "cible à notifier (répétable, ou en liste : --target a,b)")
	f.StringArrayVar(&severities, "severity", nil, "sévérités à capter : info, notice, warning, error, unknown")
	f.StringArrayVar(&fields, "match-field", nil, "champ de métadonnée, forme « (regex|exact):<champ>=<valeur> »")
	f.StringArrayVar(&calendars, "match-calendar", nil, "fenêtre horaire, format calendrier systemd")
	f.StringVar(&mode, "mode", "all", "all : tous les critères doivent tenir ; any : un seul suffit (forcé dès 2 sévérités)")
	f.BoolVar(&invert, "invert", false, "inverse le résultat du matcher entier")
	f.StringVar(&comment, "comment", "", "description de la règle")
	addWriteFlags(c)
	addRenderFlags(c)
	return c
}

func matcherCriteriaLabel(o pve.MatcherOptions) string {
	if len(o.Severities) > 0 {
		return "les notifications de sévérité " + strings.Join(o.Severities, "|")
	}
	if len(o.Fields) > 0 || len(o.Calendars) > 0 {
		return "les notifications filtrées"
	}
	return "TOUTES les notifications"
}

func newNotifyMatcherRmCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "rm <nom>",
		Aliases: []string{"delete"},
		Short:   "Supprime une règle de routage (DELETE /cluster/notifications/matchers/{name})",
		Long: `Supprime un matcher.

Les cibles qu'il routait restent déclarées et cessent de recevoir. Rien ne
signale ce silence : c'est la panne la plus discrète de cette famille, et la
raison d'être de « notify target test ».

Endpoint : DELETE /api2/json/cluster/notifications/matchers/{name} (Sys.Modify sur /)`,
		Args: usage(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			runner := newRunner(cmd, client)

			_, err = runner.Run(cmd.Context(), service.Mutation{
				Target:      name,
				Destructive: true,
				Plan: service.Plan{
					Method:   "DELETE",
					Path:     pve.NotifyMatcherPath(name),
					Effect:   fmt.Sprintf("supprime la règle « %s » : ses cibles cesseront de recevoir, sans le dire", name),
					Rollback: "pvecli notify matcher create " + name + " --target … --severity …",
					Verify:   "la règle doit disparaître de /cluster/notifications/matchers",
				},
				PreRead: func(ctx context.Context) (service.State, error) {
					m, err := client.NotifyMatcherByName(ctx, name)
					if err != nil {
						return service.State{}, err
					}
					if m.Builtin() {
						return service.State{}, fmt.Errorf(
							"« %s » est le matcher intégré de PVE, il ne se supprime pas", name)
					}
					return service.State{Exists: true, Status: "présent", Raw: *m}, nil
				},
				Write: func(ctx context.Context) (string, error) {
					return "", client.DeleteNotifyMatcher(ctx, name)
				},
				PostRead: func(ctx context.Context) (service.State, error) {
					matchers, err := client.NotifyMatchers(ctx)
					if err != nil {
						return service.State{}, err
					}
					for _, m := range matchers {
						if m.Name == name {
							return service.State{}, fmt.Errorf(
								"le matcher %q est toujours là, la suppression n'a rien fait", name)
						}
					}
					return service.State{Exists: true, Status: "supprimé"}, nil
				},
			})
			return err
		},
	}
	addWriteFlags(c)
	return c
}

// splitCommas aplatit les drapeaux répétables qui acceptent aussi une liste à
// virgules, pour que --target a,b et --target a --target b soient équivalents.
func splitCommas(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		for _, part := range strings.Split(item, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
