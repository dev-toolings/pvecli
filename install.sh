#!/bin/sh
# Installe pvecli depuis les releases GitHub.
#
#   curl -fsSL https://raw.githubusercontent.com/dev-toolings/pvecli/main/install.sh | sh
#
# Variables :
#   PVECLI_VERSION        version précise (défaut : la dernière release)
#   PREFIX                racine d'installation (défaut : ~/.local → ~/.local/bin)
#   PVECLI_NO_AGENT       =1 pour ne pas installer l'agent Claude Code
#   PVECLI_NO_SHELL_HOOK  =1 pour ne pas câbler la notification de mise à
#                         jour dans le shell (voir « pvecli update install-hook »)
#   PVECLI_NO_SETUP       =1 pour installer sans proposer la première
#                         configuration (adresse, TLS, token)
#   PVECLI_ONLY_IF_NEWER  =1 pour ne rien faire si la version visée est déjà
#                         installée. C'est ce qui rend ce script rejouable
#                         sans frais depuis un timer (voir scripts/autoupdate/).
#                         Dans ce mode la première configuration n'est pas
#                         proposée : un timer ne doit pas poser de questions.
#
# CE SCRIPT VÉRIFIE LA SOMME SHA-256 AVANT D'INSTALLER, ET S'ARRÊTE SI ELLE NE
# CORRESPOND PAS. Un installeur qu'on canalise dans un shell exécute du code
# arrivé par le réseau ; le minimum qu'il doive à celui qui le lance, c'est de
# prouver que l'octet posé sur son disque est bien celui qui a été publié.
# C'est la même exigence que l'épinglage TLS de la CLI elle-même : vérifier
# coûte peu et ne se rattrape pas après coup.
set -eu

REPO="dev-toolings/pvecli"
BINARY="pvecli"
PREFIX="${PREFIX:-$HOME/.local}"
BINDIR="$PREFIX/bin"

say() { printf '%s\n' "$*"; }
die() {
	printf '\n✗ %s\n' "$*" >&2
	exit 1
}

# ── Première configuration ────────────────────────────────────────────────────
#
# Proposée ici, et pas par une sous-commande Go. `curl` lit ce fichier sur
# main, puis lance le binaire de la dernière release : une commande nouvelle
# n'existerait qu'au prochain tag. config init, config trust, config set,
# login, auth set-secret et doctor sont déjà dans ce binaire.
#
# curl | sh a déjà consommé l'entrée standard. Les questions et les commandes
# interactives lisent /dev/tty, le terminal contrôlant, pas stdin. Un échec de
# cette offre ne fait pas échouer l'installation : le binaire est en place.

# Même ordre que internal/config.Path, sans le flag --config :
# $PVECLI_CONFIG, puis $XDG_CONFIG_HOME/pvecli/, puis ~/.config/pvecli/.
config_file() {
	if [ -n "${PVECLI_CONFIG:-}" ]; then
		printf '%s\n' "$PVECLI_CONFIG"
		return 0
	fi
	if [ -n "${XDG_CONFIG_HOME:-}" ]; then
		printf '%s\n' "$XDG_CONFIG_HOME/pvecli/config.yaml"
		return 0
	fi
	printf '%s\n' "$HOME/.config/pvecli/config.yaml"
}

# Vrai seulement si un terminal contrôlant est joignable. Ouvrir /dev/tty
# échoue quand il n'y en a pas (timer, ssh sans -t) : on n'attend alors pas
# une réponse qui ne viendra pas.
have_tty() {
	( : <>/dev/tty ) 2>/dev/null
}

# Invite sur le terminal. REPLY reçoit la ligne, bords coupés. Retourne 1 si
# le terminal se ferme pendant la lecture.
tty_ask() {
	printf '%s' "$1" >/dev/tty
	if ! IFS= read -r REPLY </dev/tty; then
		REPLY=""
		return 1
	fi
	REPLY=$(printf '%s' "$REPLY" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
}

# Mêmes oui que cmd/confirm.go. Vide, ou toute autre réponse, vaut non.
tty_yes() {
	case "$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')" in
	o | oui | y | yes) return 0 ;;
	*) return 1 ;;
	esac
}

# Ajoute le schéma et le port quand on te les épargne. Refuse le vide et
# l'espace : le résultat part en argument, pas dans un eval.
normalize_endpoint() {
	ep=$(printf '%s' "$1" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
	case "$ep" in
	"" | *[[:space:]]*) return 1 ;;
	*://*) printf '%s\n' "$ep" ;;
	*:*) printf 'https://%s\n' "$ep" ;;
	*) printf 'https://%s:8006\n' "$ep" ;;
	esac
}

agent_line() {
	cat <<'EOF'
L'agent s'invoque depuis Claude Code : « crée-moi une VM 4 vCPU 16 Go ».
EOF
}

manual_next() {
	cat <<'EOF'

Première configuration, à la main :

  pvecli config init --endpoint https://pve.example:8006 --node pve
  pvecli config trust
  pvecli login --user root@pam
  pvecli doctor

Si le certificat porte un autre nom que l'adresse (Tailscale, ACME),
à la place de « config trust » :

  pvecli config set tls.server_name NOM

Si le token existe déjà, à la place de « login » :

  pvecli config set token_id 'user@realm!token'
  pvecli auth set-secret

« config init » refuse d'écraser un fichier déjà présent.

EOF
	agent_line
}

# $* : pourquoi on s'arrête. Le binaire reste installé, le code de sortie
# de l'installeur reste 0.
setup_stopped() {
	say ""
	say "$*"
	say "pvecli est installé."
	if [ -e "$(config_file)" ]; then
		say "Le fichier $(config_file) existe : ne relance pas « config init » sans --force."
	fi
	manual_next
}

# Lance le binaire qu'on vient de poser, clavier branché sur le terminal.
pve() {
	"$BINDIR/$BINARY" "$@" </dev/tty
}

offer_first_config() {
	cfg=$(config_file)

	if [ -e "$cfg" ]; then
		say ""
		say "Installé."
		say "Configuration déjà présente ($cfg), laissée telle quelle."
		say ""
		say "  pvecli doctor"
		say ""
		agent_line
		return 0
	fi

	if [ "${PVECLI_NO_SETUP:-}" = "1" ] || [ "${PVECLI_ONLY_IF_NEWER:-}" = "1" ] || ! have_tty; then
		say ""
		say "Installé."
		manual_next
		return 0
	fi

	say ""
	say "Installé."
	say ""
	if ! tty_ask "Configurer ce poste pour parler au nœud ? [o/N] "; then
		setup_stopped "Saisie interrompue."
		return 0
	fi
	if ! tty_yes "$REPLY"; then
		manual_next
		return 0
	fi

	if [ ! -x "$BINDIR/$BINARY" ]; then
		setup_stopped "Binaire introuvable : $BINDIR/$BINARY"
		return 0
	fi

	endpoint=""
	while [ -z "$endpoint" ]; do
		if ! tty_ask "Adresse de l'API (ex. https://pve.example:8006) : "; then
			setup_stopped "Saisie interrompue."
			return 0
		fi
		if ! endpoint=$(normalize_endpoint "$REPLY"); then
			printf '%s\n' "Adresse vide ou illisible. Exemple : https://pve.example:8006" >/dev/tty
			endpoint=""
		fi
	done

	if ! tty_ask "Nom du nœud [pve] : "; then
		setup_stopped "Saisie interrompue."
		return 0
	fi
	node="$REPLY"
	if [ -z "$node" ]; then
		node="pve"
	fi
	case "$node" in
	*[[:space:]]*)
		setup_stopped "Nom de nœud refusé (il contient un espace)."
		return 0
		;;
	esac

	say "→ endpoint $endpoint"
	say "→ nœud $node"

	if ! pve config init --endpoint "$endpoint" --node "$node"; then
		setup_stopped "« config init » n'a pas abouti."
		return 0
	fi

	cat >/dev/tty <<'EOF'

Certificat TLS :
  1  épingler celui que le nœud présente (auto-signé, ou le nom est l'adresse)
  2  le nom public n'est pas l'adresse (Tailscale, ACME : ne pas épingler)
EOF
	tls=""
	while [ -z "$tls" ]; do
		if ! tty_ask "Choix [1] : "; then
			setup_stopped "Saisie interrompue."
			return 0
		fi
		[ -n "$REPLY" ] || REPLY=1
		case "$REPLY" in
		1 | 2) tls="$REPLY" ;;
		*) printf '%s\n' "Réponds 1 ou 2." >/dev/tty ;;
		esac
	done

	if [ "$tls" = "1" ]; then
		if ! pve config trust; then
			setup_stopped "Le certificat n'a pas été épinglé."
			return 0
		fi
	else
		sni=""
		while [ -z "$sni" ]; do
			if ! tty_ask "Nom du certificat (ex. pve.exemple.ts.net) : "; then
				setup_stopped "Saisie interrompue."
				return 0
			fi
			sni="$REPLY"
			case "$sni" in
			"" | *[[:space:]]*)
				printf '%s\n' "Nom vide ou illisible." >/dev/tty
				sni=""
				;;
			esac
		done
		if ! pve config set tls.server_name "$sni"; then
			setup_stopped "tls.server_name n'a pas été écrit."
			return 0
		fi
	fi

	cat >/dev/tty <<'EOF'

Token d'API :
  1  j'ai déjà l'identifiant et le secret
  2  le créer avec le mot de passe root du nœud
EOF
	tok=""
	while [ -z "$tok" ]; do
		if ! tty_ask "Choix [1] : "; then
			setup_stopped "Saisie interrompue."
			return 0
		fi
		[ -n "$REPLY" ] || REPLY=1
		case "$REPLY" in
		1 | 2) tok="$REPLY" ;;
		*) printf '%s\n' "Réponds 1 ou 2." >/dev/tty ;;
		esac
	done

	if [ "$tok" = "1" ]; then
		tid=""
		while [ -z "$tid" ]; do
			if ! tty_ask "Identifiant (ex. automation@pve!pvectl) : "; then
				setup_stopped "Saisie interrompue."
				return 0
			fi
			tid="$REPLY"
			case "$tid" in
			"" | *[[:space:]]*)
				printf '%s\n' "Identifiant vide ou illisible." >/dev/tty
				tid=""
				;;
			*!* ) ;;
			*)
				printf '%s\n' "Forme attendue : user@realm!token" >/dev/tty
				tid=""
				;;
			esac
		done
		if ! pve config set token_id "$tid"; then
			setup_stopped "token_id n'a pas été écrit."
			return 0
		fi
		if ! pve auth set-secret; then
			setup_stopped "Le secret n'a pas été rangé dans le trousseau."
			return 0
		fi
	else
		say ""
		say "Le token créé n'aura que PVEAdmin sur /. Les rôles du nœud se posent ensuite, en root."
		if ! pve login --user root@pam; then
			setup_stopped "« login » n'a pas abouti."
			return 0
		fi
		say ""
		say "Ce token n'a que PVEAdmin sur /."
		say "Redémarrage : rôle node-powermgmt sur /nodes/$node."
		say "Index apt : rôle node-sysmodify sur /nodes/$node."
		say "Ça s'ajoute sur le nœud, en root (pveum). Ce token ne peut pas se les donner."
	fi

	say ""
	say "→ pvecli doctor"
	if ! pve doctor; then
		say ""
		say "doctor n'est pas vert. pvecli est installé ; relance « pvecli doctor » après correction."
		say ""
		agent_line
		return 0
	fi

	say ""
	say "Première configuration écrite. « pvecli vm ls » est la première vraie commande."
	say ""
	agent_line
}

# Réservé aux tests : n'installe rien, ne lance que l'offre. Sans effet tant
# que la variable n'est pas posée.
if [ "${PVECLI_TEST_OFFER:-}" = "1" ]; then
	offer_first_config
	exit 0
fi

# ── Outils ────────────────────────────────────────────────────────────────────

have() { command -v "$1" >/dev/null 2>&1; }

# Les téléchargements échouent en silence : chaque appel est suivi d'un message
# qui nomme ce qui manque et pourquoi. Laisser en plus passer « curl: (22) The
# requested URL returned error: 404 » ne dit rien de plus et brouille le vrai.
if have curl; then
	fetch() { curl -fsL "$1" -o "$2" 2>/dev/null; }
	fetch_stdout() { curl -fsL "$1" 2>/dev/null; }
elif have wget; then
	fetch() { wget -qO "$2" "$1" 2>/dev/null; }
	fetch_stdout() { wget -qO- "$1" 2>/dev/null; }
else
	die "ni curl ni wget — impossible de télécharger quoi que ce soit."
fi

# sha256sum sous Linux, shasum -a 256 sous macOS. Sans l'un des deux on
# n'installe pas : le contrôle n'est pas une option de confort.
if have sha256sum; then
	sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif have shasum; then
	sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
	die "ni sha256sum ni shasum — la somme de contrôle ne peut pas être vérifiée,
  et installer sans la vérifier n'est pas proposé par ce script."
fi

# ── Plateforme ────────────────────────────────────────────────────────────────

os="$(uname -s)"
arch="$(uname -m)"

case "$os/$arch" in
Linux/x86_64 | Linux/amd64) target="linux_amd64" ;;
Darwin/arm64 | Darwin/aarch64) target="darwin_arm64" ;;
*)
	die "plateforme non publiée : $os/$arch

  Les releases couvrent linux/amd64 et darwin/arm64. Pour tout le reste, la
  compilation depuis les sources prend une commande :

    git clone https://github.com/$REPO.git && cd $BINARY && make install"
	;;
esac

# ── Version ───────────────────────────────────────────────────────────────────

version="${PVECLI_VERSION:-}"
if [ -z "$version" ]; then
	say "→ recherche de la dernière release de $REPO"
	# L'API renvoie du JSON ; on n'extrait qu'un champ, sans dépendre de jq.
	version="$(fetch_stdout "https://api.github.com/repos/$REPO/releases/latest" |
		sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)"
	[ -n "$version" ] || die "aucune release trouvée pour $REPO.

  Le dépôt n'en publie peut-être pas encore. Depuis les sources :
    git clone https://github.com/$REPO.git && cd $BINARY && make install"
fi

asset="${BINARY}_${version}_${target}"
base="https://github.com/$REPO/releases/download/$version"

# Rejoué chaque jour par un timer, ce script téléchargerait 13 Mo pour réécrire
# le même octet. `pvecli --version` imprime « pvecli v0.1.0 (commit abc1234) » ;
# le premier champ suffit à trancher. On compare des chaînes, pas des numéros :
# on ne cherche pas « plus récent », seulement « autre chose que ce qui tourne ».
if [ "${PVECLI_ONLY_IF_NEWER:-}" = "1" ] && [ -x "$BINDIR/$BINARY" ]; then
	installed="$("$BINDIR/$BINARY" --version 2>/dev/null | awk '{print $2}')"
	if [ "$installed" = "$version" ]; then
		say "→ pvecli $version déjà installé — rien à faire"
		exit 0
	fi
	# Un binaire « dev » vient d'un `make install` : quelqu'un travaille dessus,
	# et il contient presque toujours plus que la dernière release. Le remplacer
	# depuis un timer, la nuit, sans que personne ne l'ait demandé, ferait
	# disparaître ce travail — et la panne du lendemain serait cherchée partout
	# sauf ici. On s'abstient, et on le dit.
	if [ "$installed" = "dev" ]; then
		say "→ pvecli compilé localement (dev) — laissé en place, $version non installée"
		say "  pour repasser sur la release publiée :  PVECLI_ONLY_IF_NEWER= sh install.sh"
		exit 0
	fi
	say "→ pvecli ${installed:-absent} → $version"
fi

say "→ pvecli $version ($target)"

# ── Téléchargement et vérification ────────────────────────────────────────────

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

say "→ téléchargement"
fetch "$base/$asset" "$tmp/$asset" ||
	die "téléchargement impossible : $base/$asset

  Cette version publie-t-elle bien un binaire pour $target ?"
fetch "$base/SHA256SUMS" "$tmp/SHA256SUMS" ||
	die "SHA256SUMS introuvable pour $version — installation refusée.

  Sans lui, rien ne distingue le binaire publié d'un fichier substitué en route."

expected="$(grep -F " $asset" "$tmp/SHA256SUMS" | cut -d' ' -f1 | head -1)"
[ -n "$expected" ] || die "$asset n'apparaît pas dans SHA256SUMS — installation refusée."

actual="$(sha256 "$tmp/$asset")"
if [ "$expected" != "$actual" ]; then
	die "SOMME DE CONTRÔLE INVALIDE — rien n'a été installé.

  attendue  $expected
  obtenue   $actual

  Soit le téléchargement a été corrompu, soit le fichier n'est pas celui qui a
  été publié. Recommence ; si l'écart persiste, ne l'installe pas."
fi
say "  ✓ SHA-256 vérifiée"

# ── Installation ──────────────────────────────────────────────────────────────

mkdir -p "$BINDIR"
chmod 0755 "$tmp/$asset"
# On écrit à côté puis on déplace : un binaire remplacé pendant qu'il tourne ne
# doit jamais être un fichier à moitié écrit.
mv "$tmp/$asset" "$BINDIR/$BINARY.new"
mv "$BINDIR/$BINARY.new" "$BINDIR/$BINARY"
say "  ✓ $BINDIR/$BINARY"

# La preuve est l'exécution, pas la copie.
got="$("$BINDIR/$BINARY" --version)" || die "le binaire installé ne s'exécute pas."
say "  ✓ $got"

# ── Notification de mise à jour ───────────────────────────────────────────────
#
# Câble la notification de mise à jour (PVX-090), désormais une fonctionnalité
# du binaire lui-même : ce script n'a jamais le dépôt sous la main, seulement
# le binaire qu'il vient de poser, et « pvecli update install-hook » embarque
# tout ce qu'il faut. Un échec ici n'est pas un échec d'installation : pvecli
# est installé, c'est l'essentiel — le hook n'est qu'un confort.
if [ "${PVECLI_NO_SHELL_HOOK:-}" != "1" ]; then
	if hook_out="$("$BINDIR/$BINARY" update install-hook 2>/dev/null)"; then
		say "  · $(printf '%s' "$hook_out" | head -1)"
	else
		say "  · notification non câblée — « pvecli update install-hook » à la main"
	fi
fi

# ── Agent Claude Code ─────────────────────────────────────────────────────────

if [ "${PVECLI_NO_AGENT:-}" != "1" ]; then
	# On rend le chemin que la commande a réellement écrit, pas celui qu'on
	# suppose : CLAUDE_CONFIG_DIR peut déplacer la cible.
	if agent_out="$("$BINDIR/$BINARY" ai install 2>/dev/null)"; then
		say "  ✓ $(printf '%s' "$agent_out" | head -1)"
	else
		# Un agent déjà personnalisé n'est pas un échec d'installation.
		say "  · agent non installé (déjà présent et modifié ?) — « pvecli ai status »"
	fi
fi

# ── Suite ─────────────────────────────────────────────────────────────────────

case ":$PATH:" in
*":$BINDIR:"*) ;;
*)
	say ""
	say "⚠ $BINDIR n'est pas dans ton PATH. Ajoute :"
	say ""
	say "    export PATH=\"\$PATH:$BINDIR\""
	;;
esac

# L'offre lit /dev/tty. Sans terminal, sans accord, ou si la configuration
# existe déjà, elle imprime les commandes et rend la main. Elle ne passe
# jamais --force.
offer_first_config
