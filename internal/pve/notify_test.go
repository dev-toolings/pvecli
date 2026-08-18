package pve

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Ce que ces tests protègent : les quatre détails de forme qui, chacun, donnent
// une chaîne d'alerte qui a l'air correcte et ne délivre rien. Aucun d'eux ne
// se voit à l'exécution, et c'est toute la difficulté de cette famille.

func TestDiscordWebhookSplitsSecretsOutOfTheURL(t *testing.T) {
	// Le nœud valide « url » contre une regex d'URL. Un gabarit entier y est
	// refusé, donc l'URL doit rester une vraie URL et le jeton partir dans un
	// secret. Écrire l'URL complète en clair passerait le nœud et exposerait
	// le jeton dans chaque « notify webhook ls ».
	o, err := DiscordWebhook("discord", "https://discord.com/api/webhooks/123456/le-jeton", "alertes")
	if err != nil {
		t.Fatalf("DiscordWebhook: %v", err)
	}
	if strings.Contains(o.URL, "le-jeton") {
		t.Fatalf("le jeton ne doit jamais rester dans l'URL : %q", o.URL)
	}
	if o.Secrets["id"] != "123456" || o.Secrets["token"] != "le-jeton" {
		t.Fatalf("secrets mal découpés : %+v", o.Secrets)
	}
	if !strings.Contains(o.URL, "{{ secrets.id }}") || !strings.Contains(o.URL, "{{ secrets.token }}") {
		t.Fatalf("l'URL doit référencer les deux secrets : %q", o.URL)
	}

	// Discord rejette une requête sans Content-Type JSON, en silence côté
	// salon : rien n'apparaît, et le nœud n'a rien à signaler.
	if o.Headers["Content-Type"] != "application/json" {
		t.Fatalf("en-tête manquant : %+v", o.Headers)
	}

	// Sans « escape », un titre contenant un guillemet fabrique un JSON
	// invalide, donc l'alerte disparaît le jour où elle a quelque chose à
	// dire.
	if !strings.Contains(o.Body, "escape title") {
		t.Fatalf("le gabarit doit échapper le titre : %q", o.Body)
	}
}

func TestDiscordEmbedBodyStaysSmallAndRenders(t *testing.T) {
	// Le corps ne doit JAMAIS transporter « message ». Un rapport vzdump est
	// plafonné à 1 MiB côté PVE, un embed Discord à 4096 caractères de
	// description : l'inclure ferait échouer la requête en 400 les jours de
	// gros incident, donc pile quand l'alerte comptait.
	if strings.Contains(DiscordEmbedBody, "message") {
		t.Fatal("le gabarit ne doit pas transporter le message : il peut peser 1 MiB")
	}

	// Discord refuse un champ de valeur vide, et « notify target test »
	// n'envoie aucune métadonnée. Chaque champ optionnel doit donc avoir un
	// repli, sinon la commande censée PROUVER la chaîne est la seule à échouer.
	for _, guarded := range []string{"fields.hostname", "fields.type"} {
		if !strings.Contains(DiscordEmbedBody, "{{#if "+guarded+" }}") {
			t.Errorf("%s doit avoir un repli explicite", guarded)
		}
	}

	// Le tiret de « job-id » doit être entre crochets : sans eux, le moteur de
	// gabarit lit une soustraction et le rendu échoue en silence.
	if !strings.Contains(DiscordEmbedBody, "fields.[job-id]") {
		t.Error("job-id doit être écrit entre crochets")
	}

	// Un rendu où toutes les variables sont substituées doit être du JSON
	// valide. C'est ce que le nœud enverra vraiment.
	rendered := DiscordEmbedBody
	for _, r := range []struct{ from, to string }{
		{"{{ escape title }}", "vzdump backup status (pve.home): backup failed"},
		{"{{#if fields.hostname }}{{ escape fields.hostname }}{{else}}n/a{{/if}}", "pve"},
		{"{{#if fields.type }}{{ escape fields.type }}{{else}}test{{/if}}", "vzdump"},
		{"{{ escape severity }}", "error"},
	} {
		rendered = strings.Replace(rendered, r.from, r.to, 1)
	}
	// La branche conditionnelle du job planifié, non prise.
	if i := strings.Index(rendered, "{{#if fields.[job-id] }}"); i >= 0 {
		j := strings.Index(rendered, "{{/if}}")
		rendered = rendered[:i] + rendered[j+len("{{/if}}"):]
	}
	if strings.Contains(rendered, "{{") {
		t.Fatalf("variable non couverte par le test : %s", rendered)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(rendered), &out); err != nil {
		t.Fatalf("le rendu n'est pas du JSON valide : %v\n%s", err, rendered)
	}
	if len(rendered) > 1000 {
		t.Errorf("rendu de %d octets : trop gros pour un gabarit à taille fixe", len(rendered))
	}
}

func TestDiscordWebhookRejectsForeignHost(t *testing.T) {
	// Une URL d'un autre hôte est presque toujours un copier-coller raté. La
	// laisser passer poserait une cible qui accepte tout et ne délivre rien.
	if _, err := DiscordWebhook("d", "https://exemple.test/api/webhooks/1/2", ""); err == nil {
		t.Fatal("un hôte étranger doit être refusé")
	}
	if _, err := DiscordWebhook("d", "https://discord.com/api/webhooks/", ""); err == nil {
		t.Fatal("une URL sans identifiant ni jeton doit être refusée")
	}

	// Et l'erreur ne doit pas recracher l'URL : elle finirait dans un journal.
	_, err := DiscordWebhook("d", "https://exemple.test/api/webhooks/123/tres-secret", "")
	if err != nil && strings.Contains(err.Error(), "tres-secret") {
		t.Fatalf("l'erreur emporte le jeton : %v", err)
	}
}

func TestWebhookOptionsEncodesHeadersAndSecrets(t *testing.T) {
	// La configuration PVE est faite de property strings à virgules et signes
	// égal : un « application/json » brut y casserait le parseur. L'encodage
	// est fait une fois ici pour que personne n'ait à s'en souvenir.
	o := WebhookOptions{
		Name:    "w",
		URL:     "https://exemple.test/hook",
		Headers: map[string]string{"Content-Type": "application/json"},
		Secrets: map[string]string{"token": "s3cr3t"},
		Body:    `{"content":"x"}`,
	}
	v := o.Values()

	wantHeader := "name=Content-Type,value=" + base64.StdEncoding.EncodeToString([]byte("application/json"))
	if got := v.Get("header"); got != wantHeader {
		t.Fatalf("header = %q, want %q", got, wantHeader)
	}
	if got := v.Get("secret"); !strings.HasPrefix(got, "name=token,value=") || strings.Contains(got, "s3cr3t") {
		t.Fatalf("le secret doit partir encodé, pas en clair : %q", got)
	}
	if got := v.Get("body"); got != base64.StdEncoding.EncodeToString([]byte(`{"content":"x"}`)) {
		t.Fatalf("body non encodé : %q", got)
	}
	// La méthode par défaut est POST : un webhook en GET ne transporte pas de
	// corps, donc n'alerte de rien.
	if v.Get("method") != "post" {
		t.Fatalf("méthode par défaut = %q", v.Get("method"))
	}
}

func TestMatcherOptionsRepeatsKeysInsteadOfJoining(t *testing.T) {
	// « match-severity=warning,error » est ACCEPTÉ par le nœud, stocké comme
	// une seule valeur, et ne correspond alors à aucune notification. C'est le
	// bug le plus silencieux de cette famille : la règle existe, elle est
	// visible, elle ne matche jamais.
	o := MatcherOptions{
		Name:       "m",
		Targets:    []string{"discord", "mail-to-root"},
		Severities: []string{"warning", "error"},
	}
	v := o.Values()
	if got := v["match-severity"]; len(got) != 2 {
		t.Fatalf("les sévérités doivent partir en clés répétées, reçu %v", got)
	}
	if got := v["target"]; len(got) != 2 {
		t.Fatalf("les cibles doivent partir en clés répétées, reçu %v", got)
	}
}

func TestNotifyTargetDefaultsToEnabled(t *testing.T) {
	// « disable » absent veut dire ACTIF. Inverser ce défaut ferait afficher
	// « désactivé » sur toutes les cibles saines d'un nœud neuf.
	if !(NotifyTarget{}).IsEnabled() {
		t.Fatal("une cible sans champ disable est active")
	}
	off := flexInt(1)
	if (NotifyTarget{Disable: &off}).IsEnabled() {
		t.Fatal("disable=1 doit désactiver")
	}
}

func TestNotifyMatcherCriteriaNamesTheCatchAll(t *testing.T) {
	// Un matcher sans critère prend TOUT. Rendre une colonne vide le ferait
	// passer pour anodin, alors que c'est le comportement le plus lourd.
	if got := (NotifyMatcher{}).Criteria(); got != "tout" {
		t.Fatalf("critère = %q, want \"tout\"", got)
	}
	m := NotifyMatcher{MatchSeverity: []string{"warning", "error"}}
	if got := m.Criteria(); !strings.Contains(got, "warning|error") {
		t.Fatalf("critère = %q", got)
	}
}

func TestNotifyWebhookDecodesBodyAndHidesSecretValues(t *testing.T) {
	w := NotifyWebhook{
		Body:   base64.StdEncoding.EncodeToString([]byte(`{"content":"x"}`)),
		Secret: []string{"name=token", "name=id"},
	}
	if got := w.DecodedBody(); got != `{"content":"x"}` {
		t.Fatalf("corps = %q", got)
	}
	names := w.SecretNames()
	if len(names) != 2 || names[0] != "token" {
		t.Fatalf("noms de secrets = %v", names)
	}
}

func TestNotifyRoutesAndVerbs(t *testing.T) {
	// La route et le verbe : une faute ici ne se voit que contre un vrai nœud,
	// et se traduit par un 501 ou un 404 qui accuse la fonctionnalité plutôt
	// que le chemin.
	c, calls := fwServer(t, func(r *http.Request) string {
		if strings.HasSuffix(r.URL.Path, "/targets") {
			return `{"data":[{"name":"discord","type":"webhook","origin":"user-created"}]}`
		}
		return `{"data":{"name":"discord","url":"https://exemple.test/x","method":"post"}}`
	})
	ctx := context.Background()

	for _, tc := range []struct {
		name   string
		run    func() error
		method string
		path   string
	}{
		{"targets", func() error { _, err := c.NotifyTargets(ctx); return err },
			"GET", "/api2/json/cluster/notifications/targets"},
		{"test", func() error { return c.TestNotifyTarget(ctx, "discord") },
			"POST", "/api2/json/cluster/notifications/targets/discord/test"},
		{"webhook show", func() error { _, err := c.NotifyWebhookByName(ctx, "discord"); return err },
			"GET", "/api2/json/cluster/notifications/endpoints/webhook/discord"},
		{"webhook rm", func() error { return c.DeleteNotifyWebhook(ctx, "discord") },
			"DELETE", "/api2/json/cluster/notifications/endpoints/webhook/discord"},
		{"matcher create", func() error { return c.CreateNotifyMatcher(ctx, MatcherOptions{Name: "m"}) },
			"POST", "/api2/json/cluster/notifications/matchers"},
		{"matcher rm", func() error { return c.DeleteNotifyMatcher(ctx, "m") },
			"DELETE", "/api2/json/cluster/notifications/matchers/m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			got := lastCall(t, calls)
			if got.method != tc.method || got.path != tc.path {
				t.Fatalf("%s %s, want %s %s", got.method, got.path, tc.method, tc.path)
			}
		})
	}
}
