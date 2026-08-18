package pve

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Le sous-système de NOTIFICATION est le seul endroit de PVE où l'absence de
// configuration ne se voit pas.
//
// Tout le reste de cette CLI décrit des objets qui existent ou n'existent pas :
// une VM éteinte se voit, un job sans prochaine exécution se voit. Une chaîne
// d'alerte, elle, a exactement la même tête qu'elle fonctionne ou non : dans les
// deux cas, il ne se passe rien. C'est ce qui justifie que « target test » soit
// une commande à part entière plutôt qu'une option : sans envoi réel, une cible
// déclarée ne prouve rien.
//
// Le nœud sort d'installation avec UNE cible, « mail-to-root », qui poste vers
// la boîte locale de root@pam. Sur un lab sans MTA sortant, cela veut dire que
// les échecs de sauvegarde sont notifiés à un endroit que personne ne lit
// jamais. Le RPO est intact, la boucle de rétroaction est coupée.
//
// Schéma vérifié contre le nœud du lab en PVE 9.2.6 le 18-08-2026
// (« pvesh usage /cluster/notifications/… --verbose »).

// NotifyTarget est une cible de notification, tous types confondus.
//
// PVE range les cibles par type dans des endpoints séparés
// (…/endpoints/webhook, …/endpoints/gotify, …/endpoints/smtp), mais expose une
// vue unifiée en lecture seule sur …/targets. C'est celle-ci qui répond à la
// question qu'on se pose vraiment : qu'est-ce qui est branché sur ce nœud.
type NotifyTarget struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Comment string   `json:"comment"`
	Origin  string   `json:"origin"`
	Disable *flexInt `json:"disable"`
}

// IsEnabled applique le défaut du schéma (disable=0, donc actif) quand le champ
// est absent.
func (t NotifyTarget) IsEnabled() bool { return t.Disable == nil || *t.Disable == 0 }

// Builtin dit si la cible est celle que PVE pose lui-même à l'installation.
// Elle ne se supprime pas, et elle ne prouve rien : sur un nœud sans MTA, elle
// écrit dans une boîte locale que personne n'ouvre.
func (t NotifyTarget) Builtin() bool { return t.Origin == "builtin" }

// NotifyWebhook est une cible de type webhook.
//
// Les secrets ne reviennent JAMAIS avec leur valeur : le nœud rend
// « name=token » sans le « value= ». C'est voulu, et c'est ce qui rend cette
// cible sûre à lister, d'où le choix de porter l'URL sensible dans un secret
// plutôt qu'en clair dans le champ URL.
type NotifyWebhook struct {
	Name    string   `json:"name"`
	URL     string   `json:"url"`
	Method  string   `json:"method"`
	Comment string   `json:"comment"`
	Origin  string   `json:"origin"`
	Header  []string `json:"header"`
	Secret  []string `json:"secret"`
	Body    string   `json:"body"`
	Disable *flexInt `json:"disable"`
}

// IsEnabled applique le défaut du schéma.
func (w NotifyWebhook) IsEnabled() bool { return w.Disable == nil || *w.Disable == 0 }

// SecretNames rend les noms des secrets posés, sans leurs valeurs. C'est tout
// ce que le nœud accepte de rendre, et tout ce qu'il est prudent d'afficher.
func (w NotifyWebhook) SecretNames() []string {
	out := make([]string, 0, len(w.Secret))
	for _, s := range w.Secret {
		out = append(out, ParseOptionString(s).Get("name"))
	}
	return out
}

// DecodedBody rend le gabarit du corps en clair. PVE le stocke en base64 pour
// qu'un JSON à accolades survive au format de configuration à property strings ;
// l'afficher encodé ferait d'un « show » une devinette.
func (w NotifyWebhook) DecodedBody() string {
	raw, err := base64.StdEncoding.DecodeString(w.Body)
	if err != nil {
		return w.Body
	}
	return string(raw)
}

// NotifyMatcher route les notifications vers des cibles.
//
// Sans matcher, une cible ne reçoit rien : déclarer un webhook et s'arrêter là
// est l'erreur qui donne l'impression d'avoir posé une alerte. Le matcher
// intégré « default-matcher » envoie TOUT vers mail-to-root et ne s'occupe pas
// des cibles ajoutées après lui.
type NotifyMatcher struct {
	Name          string   `json:"name"`
	Comment       string   `json:"comment"`
	Origin        string   `json:"origin"`
	Mode          string   `json:"mode"`
	Target        []string `json:"target"`
	MatchSeverity []string `json:"match-severity"`
	MatchField    []string `json:"match-field"`
	MatchCalendar []string `json:"match-calendar"`
	InvertMatch   *flexInt `json:"invert-match"`
	Disable       *flexInt `json:"disable"`
}

// IsEnabled applique le défaut du schéma.
func (m NotifyMatcher) IsEnabled() bool { return m.Disable == nil || *m.Disable == 0 }

// Builtin dit si le matcher est celui posé par PVE à l'installation.
func (m NotifyMatcher) Builtin() bool { return m.Origin == "builtin" }

// Criteria résume ce que le matcher filtre, pour une colonne de liste. Un
// matcher sans aucun critère prend TOUT : c'est le cas du default-matcher, et
// c'est une information, pas un vide.
func (m NotifyMatcher) Criteria() string {
	var parts []string
	if len(m.MatchSeverity) > 0 {
		parts = append(parts, "sévérité "+strings.Join(m.MatchSeverity, "|"))
	}
	if len(m.MatchField) > 0 {
		parts = append(parts, "champ "+strings.Join(m.MatchField, "|"))
	}
	if len(m.MatchCalendar) > 0 {
		parts = append(parts, "calendrier "+strings.Join(m.MatchCalendar, "|"))
	}
	if len(parts) == 0 {
		return "tout"
	}
	return strings.Join(parts, " + ")
}

// NotifySeverities nomme les sévérités que PVE accepte, dans l'ordre croissant.
var NotifySeverities = []string{"info", "notice", "warning", "error", "unknown"}

// ---------------------------------------------------------------- webhook

// WebhookOptions décrit un webhook à créer.
//
// Header, Secret et Body voyagent en base64 parce que la configuration PVE est
// faite de property strings à virgules et signes égal : un JSON ou un
// « application/json » brut y casserait le parseur. L'encodage est fait ici, une
// fois, pour que l'appelant n'ait jamais à y penser, et surtout pour qu'il ne
// l'oublie jamais.
type WebhookOptions struct {
	Name    string
	URL     string
	Method  string
	Comment string
	Headers map[string]string
	Secrets map[string]string
	Body    string
	Disable bool
}

// Values rend le corps de la requête.
func (o WebhookOptions) Values() url.Values {
	v := url.Values{}
	v.Set("name", o.Name)
	v.Set("url", o.URL)
	method := o.Method
	if method == "" {
		method = "post"
	}
	v.Set("method", method)
	if o.Comment != "" {
		v.Set("comment", o.Comment)
	}
	if o.Body != "" {
		v.Set("body", base64.StdEncoding.EncodeToString([]byte(o.Body)))
	}
	// Tri des clés : deux exécutions identiques doivent produire un --dry-run
	// identique, or l'itération d'une map est délibérément désordonnée en Go.
	for _, name := range sortedKeys(o.Headers) {
		v.Add("header", propertyPair(name, o.Headers[name]))
	}
	for _, name := range sortedKeys(o.Secrets) {
		v.Add("secret", propertyPair(name, o.Secrets[name]))
	}
	if o.Disable {
		v.Set("disable", "1")
	}
	return v
}

// propertyPair rend la forme « name=<nom>,value=<base64> » qu'attendent
// --header et --secret.
func propertyPair(name, value string) string {
	return "name=" + name + ",value=" + base64.StdEncoding.EncodeToString([]byte(value))
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// DiscordEmbedBody est le gabarit de corps posé par DiscordWebhook.
//
// Vérifié contre le nœud du lab le 18-08-2026, sur un envoi de test ET sur un
// vrai vzdump en échec : le rendu pèse environ 300 octets dans les deux cas,
// donc il ne peut pas franchir les limites de Discord, quel que soit l'incident.
//
// « fields.[job-id] » prend des crochets parce que le moteur lirait sinon le
// tiret comme une soustraction. Ce champ n'existe que pour un job PLANIFIÉ :
// sa présence dans le message dit donc à elle seule qu'il ne s'agit pas d'une
// sauvegarde lancée à la main.
const DiscordEmbedBody = `{"embeds":[{` +
	`"title":"{{ escape title }}",` +
	`"color":15158332,` +
	`"fields":[` +
	`{"name":"Nœud","value":"{{#if fields.hostname }}{{ escape fields.hostname }}{{else}}n/a{{/if}}","inline":true},` +
	`{"name":"Source","value":"{{#if fields.type }}{{ escape fields.type }}{{else}}test{{/if}}","inline":true},` +
	`{"name":"Sévérité","value":"{{ escape severity }}","inline":true}` +
	`{{#if fields.[job-id] }},{"name":"Job planifié","value":"` + "`" + `{{ escape fields.[job-id] }}` + "`" + `","inline":false}{{/if}}` +
	`],` +
	`"footer":{"text":"Proxmox VE · détail dans Tâches"}` +
	`}]}`

// DiscordWebhook fabrique les options d'une cible Discord à partir de l'URL de
// webhook que Discord donne.
//
// Elle existe parce que le montage à la main tombe dans trois pièges, et
// qu'aucun des trois ne rend une erreur qui le nomme :
//
//  1. Le champ « url » est validé contre une regex d'URL par le nœud. Y mettre
//     un gabarit entier (« {{ secrets.url }} ») est refusé par un « value does
//     not match the regex pattern » qui ne dit pas que le problème est le
//     gabarit. L'URL est donc coupée : la partie stable reste en clair, l'id et
//     le jeton partent dans deux secrets.
//  2. Discord n'accepte QUE du JSON, et rejette silencieusement une requête sans
//     « Content-Type: application/json ». Sans corps déclaré, PVE poste le
//     rendu texte par défaut et Discord répond 400 sans que rien n'apparaisse
//     dans le salon.
//  3. Le corps ne doit PAS transporter « message ». Un rapport vzdump est
//     plafonné à MAX_LOG_SIZE, soit 1 MiB, quand un embed Discord plafonne à
//     4096 caractères de description. Verser le message brut fait donc échouer
//     la requête en 400 exactement les jours de gros incident, c'est-à-dire
//     quand l'alerte comptait. Le gabarit porte le titre et les métadonnées ;
//     le détail reste dans les tâches du nœud, à un geste de là.
//
// LE FORMAT EST UN EMBED, PAS DU TEXTE. Discord ne rend aucun tableau markdown,
// et un bloc de code aligné défile horizontalement sur téléphone. Les champs
// « inline » d'un embed sont la seule primitive qui se réorganise seule : trois
// colonnes sur écran large, une seule sur mobile.
//
// Le gabarit passe chaque valeur par le filtre « escape » du moteur de rendu.
// Un titre contenant un guillemet fabriquerait sinon un JSON invalide, et
// l'alerte disparaîtrait le jour où elle avait quelque chose à dire. Les
// valeurs absentes ont un repli explicite : un champ vide est refusé par
// Discord, et « notify target test » n'envoie aucune métadonnée.
func DiscordWebhook(name, hookURL, comment string) (WebhookOptions, error) {
	id, token, err := splitDiscordWebhook(hookURL)
	if err != nil {
		return WebhookOptions{}, err
	}
	return WebhookOptions{
		Name:    name,
		URL:     "https://discord.com/api/webhooks/{{ secrets.id }}/{{ secrets.token }}",
		Method:  "post",
		Comment: comment,
		Headers: map[string]string{"Content-Type": "application/json"},
		Secrets: map[string]string{"id": id, "token": token},
		Body:    DiscordEmbedBody,
	}, nil
}

// splitDiscordWebhook extrait l'identifiant et le jeton d'une URL de webhook
// Discord. L'URL n'est jamais rendue dans un message d'erreur : c'est un secret,
// et une erreur finit dans un journal.
func splitDiscordWebhook(raw string) (string, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return "", "", fmt.Errorf("URL de webhook Discord illisible")
	}
	if !strings.HasSuffix(parsed.Host, "discord.com") && !strings.HasSuffix(parsed.Host, "discordapp.com") {
		return "", "", fmt.Errorf("hôte %q inattendu : une URL de webhook Discord commence par https://discord.com/api/webhooks/", parsed.Host)
	}
	// Ancrer sur le préfixe plutôt que prendre les deux derniers segments :
	// « …/api/webhooks/ » a bien deux segments, et les lire comme l'id et le
	// jeton fabriquerait une cible qui part en base64 sans que rien ne
	// proteste, jusqu'au premier 404 de Discord, que personne ne lit.
	const prefix = "/api/webhooks/"
	rest, found := strings.CutPrefix(parsed.Path, prefix)
	if !found {
		return "", "", fmt.Errorf("chemin inattendu : une URL de webhook Discord contient %q", prefix)
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("URL de webhook Discord incomplète : il manque l'identifiant ou le jeton")
	}
	return parts[0], parts[1], nil
}

// ---------------------------------------------------------------- matcher

// MatcherOptions décrit un matcher à créer.
type MatcherOptions struct {
	Name       string
	Targets    []string
	Severities []string
	Fields     []string
	Calendars  []string
	Mode       string
	Invert     bool
	Comment    string
	Disable    bool
}

// Values rend le corps de la requête.
//
// Les champs répétables partent en clés RÉPÉTÉES, pas en une chaîne à virgules.
// La différence n'est pas cosmétique : « match-severity=warning,error » est
// accepté par le nœud puis stocké comme UNE valeur, et le matcher ne
// correspond alors à rien.
func (o MatcherOptions) Values() url.Values {
	v := url.Values{}
	v.Set("name", o.Name)
	for _, t := range o.Targets {
		v.Add("target", t)
	}
	for _, s := range o.Severities {
		v.Add("match-severity", s)
	}
	for _, f := range o.Fields {
		v.Add("match-field", f)
	}
	for _, c := range o.Calendars {
		v.Add("match-calendar", c)
	}
	if o.Mode != "" {
		v.Set("mode", o.Mode)
	}
	if o.Invert {
		v.Set("invert-match", "1")
	}
	if o.Comment != "" {
		v.Set("comment", o.Comment)
	}
	if o.Disable {
		v.Set("disable", "1")
	}
	return v
}

// ---------------------------------------------------------------- client

// NotifyTargets liste toutes les cibles, triées par nom.
//
// GET /cluster/notifications/targets
func (c *Client) NotifyTargets(ctx context.Context) ([]NotifyTarget, error) {
	var out []NotifyTarget
	if err := c.get(ctx, epNotifyTargets, nil, nil, &out); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// TestNotifyTarget envoie une notification de test.
//
// C'est la seule commande de cette famille qui prouve quelque chose. Le nœud
// répond 200 dès qu'il a accepté d'essayer : un succès ici veut dire que la
// requête est partie, pas qu'elle est arrivée. La preuve finale est dans le
// salon Discord, pas dans ce retour.
//
// POST /cluster/notifications/targets/{name}/test
func (c *Client) TestNotifyTarget(ctx context.Context, name string) error {
	return c.post(ctx, epNotifyTargetTest, []string{name}, nil, nil)
}

// NotifyWebhooks liste les cibles de type webhook, triées par nom.
//
// GET /cluster/notifications/endpoints/webhook
func (c *Client) NotifyWebhooks(ctx context.Context) ([]NotifyWebhook, error) {
	var out []NotifyWebhook
	if err := c.get(ctx, epNotifyWebhooks, nil, nil, &out); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// NotifyWebhookByName lit un webhook.
//
// GET /cluster/notifications/endpoints/webhook/{name}
func (c *Client) NotifyWebhookByName(ctx context.Context, name string) (*NotifyWebhook, error) {
	var out NotifyWebhook
	if err := c.get(ctx, epNotifyWebhook, []string{name}, nil, &out); err != nil {
		return nil, err
	}
	if out.Name == "" {
		out.Name = name
	}
	return &out, nil
}

// CreateNotifyWebhook crée une cible webhook.
//
// POST /cluster/notifications/endpoints/webhook
func (c *Client) CreateNotifyWebhook(ctx context.Context, o WebhookOptions) error {
	return c.post(ctx, epNotifyWebhookNew, nil, o.Values(), nil)
}

// DeleteNotifyWebhook supprime une cible webhook.
//
// Le nœud refuse la suppression tant qu'un matcher la référence : c'est une
// bonne nouvelle, l'ordre de démontage est imposé plutôt que deviné.
//
// DELETE /cluster/notifications/endpoints/webhook/{name}
func (c *Client) DeleteNotifyWebhook(ctx context.Context, name string) error {
	return c.del(ctx, epNotifyWebhookDel, []string{name}, nil, nil)
}

// NotifyMatchers liste les matchers, triés par nom.
//
// GET /cluster/notifications/matchers
func (c *Client) NotifyMatchers(ctx context.Context) ([]NotifyMatcher, error) {
	var out []NotifyMatcher
	if err := c.get(ctx, epNotifyMatchers, nil, nil, &out); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// NotifyMatcherByName lit un matcher.
//
// GET /cluster/notifications/matchers/{name}
func (c *Client) NotifyMatcherByName(ctx context.Context, name string) (*NotifyMatcher, error) {
	var out NotifyMatcher
	if err := c.get(ctx, epNotifyMatcher, []string{name}, nil, &out); err != nil {
		return nil, err
	}
	if out.Name == "" {
		out.Name = name
	}
	return &out, nil
}

// CreateNotifyMatcher crée un matcher.
//
// POST /cluster/notifications/matchers
func (c *Client) CreateNotifyMatcher(ctx context.Context, o MatcherOptions) error {
	return c.post(ctx, epNotifyMatcherNew, nil, o.Values(), nil)
}

// DeleteNotifyMatcher supprime un matcher. Les cibles qu'il routait restent
// déclarées et ne reçoivent plus rien. Le silence qui suit ressemble à un
// fonctionnement normal.
//
// DELETE /cluster/notifications/matchers/{name}
func (c *Client) DeleteNotifyMatcher(ctx context.Context, name string) error {
	return c.del(ctx, epNotifyMatcherDel, []string{name}, nil, nil)
}

// Chemins pour --dry-run.
func NotifyWebhooksPath() string              { return epNotifyWebhooks.Pattern }
func NotifyWebhookPath(name string) string    { return epNotifyWebhook.Path(name) }
func NotifyMatchersPath() string              { return epNotifyMatchers.Pattern }
func NotifyMatcherPath(name string) string    { return epNotifyMatcher.Path(name) }
func NotifyTargetTestPath(name string) string { return epNotifyTargetTest.Path(name) }
