package cmd

import (
	"strings"
	"testing"

	"github.com/dev-toolings/pvecli/internal/testutil"
)

// Les notifications, vues depuis la CLI.
//
// Provenance des fixtures : ce sont de VRAIES captures du nœud du lab en PVE
// 9.2.6, prises le 18-08-2026 après le montage de la cible Discord. C'est ce
// qui donne leur valeur aux deux fixtures de matcher : la forme cassée y a
// réellement existé sur le nœud, et n'alertait de rien.

func TestNotifyTargetListWarnsWhenOnlyTheBuiltinExists(t *testing.T) {
	// Un nœud neuf n'a que mail-to-root, qui poste dans une boîte locale. Le
	// tableau seul est rassurant : il montre une cible. L'avertissement est
	// tout l'intérêt de la commande.
	srv := testutil.New(t, "../testdata", map[string]string{
		"GET /api2/json/cluster/notifications/targets": "notify-targets-builtin-only.json",
	})
	point(t, srv.URL)

	stdout, stderr, err := run(t, "notify", "target", "ls")
	if err != nil {
		t.Fatalf("notify target ls: %v", err)
	}
	if !strings.Contains(stdout, "mail-to-root") {
		t.Errorf("la cible intégrée doit apparaître :\n%s", stdout)
	}
	if !strings.Contains(stderr, "n'atteint personne") {
		t.Errorf("l'avertissement doit dire que personne ne lit :\n%s", stderr)
	}
}

func TestNotifyTargetListStaysQuietOnceSomethingIsPlugged(t *testing.T) {
	// Un avertissement qui se répète quand le problème est réglé est un
	// avertissement qu'on apprend à ignorer.
	srv := testutil.New(t, "../testdata", map[string]string{
		"GET /api2/json/cluster/notifications/targets": "notify-targets.json",
	})
	point(t, srv.URL)

	stdout, stderr, err := run(t, "notify", "target", "ls")
	if err != nil {
		t.Fatalf("notify target ls: %v", err)
	}
	if !strings.Contains(stdout, "discord") {
		t.Errorf("la cible ajoutée doit apparaître :\n%s", stdout)
	}
	if strings.Contains(stderr, "n'atteint personne") {
		t.Errorf("plus d'avertissement une fois une cible posée :\n%s", stderr)
	}
}

func TestNotifyWebhookListShowsSecretNamesWithoutValues(t *testing.T) {
	srv := testutil.New(t, "../testdata", map[string]string{
		"GET /api2/json/cluster/notifications/endpoints/webhook": "notify-webhooks.json",
	})
	point(t, srv.URL)

	stdout, _, err := run(t, "notify", "webhook", "ls")
	if err != nil {
		t.Fatalf("notify webhook ls: %v", err)
	}
	if !strings.Contains(stdout, "id,token") {
		t.Errorf("les NOMS des secrets doivent être lisibles :\n%s", stdout)
	}
	// L'URL stockée ne contient que des gabarits : rien de sensible ne doit
	// jamais transiter par une liste.
	if !strings.Contains(stdout, "{{ secrets.token }}") {
		t.Errorf("l'URL à gabarits doit être lisible :\n%s", stdout)
	}
}

func TestNotifyMatcherListNamesTheCatchAll(t *testing.T) {
	// « tout » n'est pas une colonne vide : c'est le default-matcher, qui prend
	// chaque notification. Le rendre invisible ferait croire à une règle
	// inoffensive.
	srv := testutil.New(t, "../testdata", map[string]string{
		"GET /api2/json/cluster/notifications/matchers": "notify-matchers.json",
	})
	point(t, srv.URL)

	stdout, _, err := run(t, "notify", "matcher", "ls")
	if err != nil {
		t.Fatalf("notify matcher ls: %v", err)
	}
	if !strings.Contains(stdout, "tout") {
		t.Errorf("le matcher sans critère doit se dire :\n%s", stdout)
	}
	if !strings.Contains(stdout, "warning|error|unknown") {
		t.Errorf("les sévérités doivent être lisibles :\n%s", stdout)
	}
}

// Le cœur de PVX-091. Mesuré sur le lab le 18-08-2026 : un matcher
// « match-severity warning,error,unknown » en « mode all » est accepté par le
// nœud, s'affiche normalement, et ne route RIEN. Un vzdump en échec ne partait
// que vers mail-to-root. « all » exige que tous les critères tiennent en même
// temps, y compris les entrées d'une même liste, et une notification ne porte
// qu'une sévérité.
func TestNotifyMatcherCreateAvoidsTheModeAllTrap(t *testing.T) {
	srv := testutil.New(t, "../testdata", map[string]string{
		"GET /api2/json/cluster/notifications/matchers": "notify-matchers.json",
		"GET /api2/json/cluster/notifications/targets":  "notify-targets.json",
	})
	point(t, srv.URL)

	// Sans --mode explicite, plusieurs sévérités veulent dire « l'une d'elles ».
	_, stderr, err := run(t, "notify", "matcher", "create", "neuf",
		"--target", "discord", "--severity", "warning,error", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "any") {
		t.Errorf("le plan doit basculer sur mode any :\n%s", stderr)
	}
	// Et les sévérités doivent partir en clés RÉPÉTÉES : jointes par une
	// virgule, le nœud les stocke comme une seule valeur qui ne matche rien.
	if strings.Contains(stderr, "warning,error") {
		t.Errorf("les sévérités ne doivent pas partir jointes :\n%s", stderr)
	}
}

func TestNotifyMatcherCreateRefusesAnExplicitModeAll(t *testing.T) {
	// Corriger en silence un choix explicite serait pire que le piège : refuser
	// nomme le problème.
	srv := testutil.New(t, "../testdata", map[string]string{
		"GET /api2/json/cluster/notifications/matchers": "notify-matchers.json",
		"GET /api2/json/cluster/notifications/targets":  "notify-targets.json",
	})
	point(t, srv.URL)

	_, _, err := run(t, "notify", "matcher", "create", "neuf",
		"--target", "discord", "--severity", "warning,error", "--mode", "all", "--dry-run")
	if err == nil {
		t.Fatal("--mode all avec deux sévérités doit être refusé")
	}
	if !strings.Contains(err.Error(), "JAMAIS") {
		t.Errorf("l'erreur doit dire que la règle ne matchera jamais : %v", err)
	}
}

func TestNotifyMatcherCreateRefusesAnUnknownTarget(t *testing.T) {
	// Le nœud accepte un matcher qui route vers une cible inexistante. La
	// règle apparaît, elle a l'air posée, et rien n'arrive nulle part.
	srv := testutil.New(t, "../testdata", map[string]string{
		"GET /api2/json/cluster/notifications/matchers": "notify-matchers.json",
		"GET /api2/json/cluster/notifications/targets":  "notify-targets.json",
	})
	point(t, srv.URL)

	_, _, err := run(t, "notify", "matcher", "create", "neuf",
		"--target", "absente", "--severity", "error", "--dry-run")
	if err == nil {
		t.Fatal("une cible inexistante doit être refusée avant l'écriture")
	}
	if !strings.Contains(err.Error(), "vide") {
		t.Errorf("l'erreur doit dire que le matcher routerait vers le vide : %v", err)
	}
}

func TestNotifyWebhookCreateNeverPrintsTheSecret(t *testing.T) {
	// Un --dry-run est fait pour être collé dans un ticket. Y laisser le jeton
	// Discord le publierait.
	srv := testutil.New(t, "../testdata", map[string]string{
		"GET /api2/json/cluster/notifications/endpoints/webhook": "notify-webhooks.json",
	})
	point(t, srv.URL)

	const token = "jeton-tres-secret"
	stdout, stderr, err := run(t, "notify", "webhook", "create", "neuf",
		"--discord", "https://discord.com/api/webhooks/123456/"+token, "--dry-run")
	if err != nil {
		t.Fatalf("dry-run: %v\n%s", err, stderr)
	}
	if strings.Contains(stdout+stderr, token) {
		t.Fatalf("le jeton a fuité dans la sortie :\n%s\n%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "discord.com") {
		t.Errorf("le plan doit quand même dire où part la requête :\n%s", stderr)
	}
}

func TestNotifyWebhookCreateRefusesAnUnusableURL(t *testing.T) {
	srv := testutil.New(t, "../testdata", map[string]string{
		"GET /api2/json/cluster/notifications/endpoints/webhook": "notify-webhooks.json",
	})
	point(t, srv.URL)

	if _, _, err := run(t, "notify", "webhook", "create", "neuf",
		"--discord", "https://exemple.test/api/webhooks/1/2", "--dry-run"); err == nil {
		t.Fatal("un hôte étranger doit être refusé")
	}
	if _, _, err := run(t, "notify", "webhook", "create", "neuf", "--dry-run"); err == nil {
		t.Fatal("sans --discord ni --url, la commande doit refuser")
	}
}
