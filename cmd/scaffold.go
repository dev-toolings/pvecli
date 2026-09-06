package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dev-toolings/pvecli/internal/catalog"
	"github.com/dev-toolings/pvecli/internal/config"
	"github.com/dev-toolings/pvecli/internal/iac"
	"github.com/dev-toolings/pvecli/internal/output"
)

// fileState is what is already on disk where an asset wants to go.
type fileState int

const (
	fileAbsent fileState = iota
	fileCurrent
	fileModified
)

func (s fileState) label() string {
	switch s {
	case fileCurrent:
		return "à jour"
	case fileModified:
		return "MODIFIÉ localement"
	default:
		return "absent"
	}
}

// scaffoldFile pairs an embedded asset with where it lands.
type scaffoldFile struct {
	asset string // path inside catalog.Assets
	dst   string // absolute path on disk
	body  []byte
	state fileState
}

func newIaCScaffoldCmd() *cobra.Command {
	var force bool

	c := &cobra.Command{
		Use:   "scaffold",
		Short: "Pose le module Terraform et les rôles Ansible du catalogue",
		Long: `Écrit, dans « iac.terraform_dir » et « iac.ansible_dir », le code que
« pvecli vm declare » alimente ensuite en données.

  <terraform_dir>/pvecli-vms.tf   variable « vms » + la ressource for_each
  <terraform_dir>/pvecli-base.tf  provider et variables partagées — SEULEMENT si
                                  le dossier ne déclare pas déjà un provider proxmox
  <ansible_dir>/pvecli.yml        un play par service, visant le groupe svc_<id>
  <ansible_dir>/roles/…           les rôles du catalogue

Le playbook s'appelle « pvecli.yml » et non « site.yml » : un dossier Ansible
existant a presque toujours le sien, écrit à la main, et les deux doivent
pouvoir cohabiter. Il se joue donc explicitement :

  pvecli iac configure --playbook pvecli.yml

La séparation est le cœur du dispositif : ces fichiers-ci sont du CODE, relu une
fois par un humain et versionné. Les VM, elles, sont de la DONNÉE, et vivent
dans pvecli.auto.tfvars.json. C'est pourquoi passer une VM de 8 à 16 Go ne
demande jamais de revenir ici.

Rien n'est écrasé. Un fichier qui diffère de la version embarquée arrête la
commande : la différence est soit une adaptation que tu as écrite, soit une
version antérieure, et choisir à ta place entre les deux ne rend service à
personne. --force tranche, après que tu aies regardé.

Pour la même raison, les collections dont les rôles ont besoin sont listées dans
« pvecli-requirements.yml » : un dossier existant a déjà son requirements.yml, et
l'écraser retirerait silencieusement les collections qu'il déclarait.

  ansible-galaxy collection install -r <ansible_dir>/pvecli-requirements.yml

La passerelle et les résolveurs du site ne sont pas écrits dans le catalogue :
ils sont rendus ici. « iac.ipv4_gateway » et « iac.dns_servers » gagnent s'ils
sont posés ; sinon le nœud est interrogé (GET /nodes/{node}/network, la
passerelle de son pont) ; sinon le défaut reste null et Terraform refuse — une
adresse plausible mais fausse route les invités vers le vide sans rien dire.`,
		Args: usage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			eff, err := resolveConfig(cmd)
			if err != nil {
				return err
			}
			if err := iac.CheckDir("iac.terraform_dir", eff.IaC.TerraformDir); err != nil {
				return err
			}
			if err := iac.CheckDir("iac.ansible_dir", eff.IaC.AnsibleDir); err != nil {
				return err
			}

			errW := cmd.ErrOrStderr()
			site := resolveSite(cmd, eff)
			_, _ = fmt.Fprintln(errW, siteLine(site))

			files, err := scaffoldPlan(eff.IaC.TerraformDir, eff.IaC.AnsibleDir, site)
			if err != nil {
				return err
			}

			var modified []string
			for _, f := range files {
				if f.state == fileModified {
					modified = append(modified, f.dst)
				}
			}
			if len(modified) > 0 && !force {
				return fmt.Errorf(`%d fichier(s) diffèrent de la version embarquée :

  %s

Regarde avant d'écraser, puis « pvecli iac scaffold --force » si tu assumes la
perte de ces modifications`, len(modified), strings.Join(modified, "\n  "))
			}

			dryRun, _ := cmd.Flags().GetBool("dry-run")
			rows := output.Rows{Headers: []string{"FICHIER", "AVANT", "ACTION"}}
			written := 0
			for _, f := range files {
				action := "écrit"
				switch {
				case f.state == fileCurrent:
					action = "inchangé"
				case dryRun:
					action = "serait écrit"
				}
				rows.Cells = append(rows.Cells, []string{f.dst, f.state.label(), action})

				if dryRun || f.state == fileCurrent {
					continue
				}
				if err := os.MkdirAll(filepath.Dir(f.dst), 0o755); err != nil {
					return fmt.Errorf("création de %s : %w", filepath.Dir(f.dst), err)
				}
				if err := os.WriteFile(f.dst, f.body, 0o644); err != nil {
					return fmt.Errorf("écriture de %s : %w", f.dst, err)
				}
				written++
			}

			if dryRun {
				_, _ = fmt.Fprintln(errW, "--dry-run : rien n'a été écrit.")
			} else {
				_, _ = fmt.Fprintf(errW, "%d fichier(s) écrit(s).\n", written)
				_, _ = fmt.Fprintf(errW, `
Ensuite, dans cet ordre :

  ansible-galaxy collection install -r %s
  pvecli vm declare <nom> --vmid <id> --cores 2 --memory 8192 --with docker
  pvecli iac plan && pvecli iac apply
  pvecli iac configure --playbook pvecli.yml --idempotence
`, filepath.Join(eff.IaC.AnsibleDir, "pvecli-requirements.yml"))
			}

			opts, err := renderOptions(cmd)
			if err != nil {
				return err
			}
			return output.Render(cmd.OutOrStdout(), opts, files2data(files), rows)
		},
	}

	addWriteFlags(c)
	addRenderFlags(c)
	c.Flags().BoolVar(&force, "force", false, "écrase les fichiers modifiés localement")
	return c
}

// scaffoldPlan resolves every embedded asset to its destination and reads what
// is already there. Nothing is written by this function: the caller decides,
// and --dry-run must be able to print the exact same list.
func scaffoldPlan(terraformDir, ansibleDir string, site iac.Site) ([]scaffoldFile, error) {
	hasProvider, err := declaresProxmoxProvider(terraformDir)
	if err != nil {
		return nil, err
	}

	var files []scaffoldFile
	err = fs.WalkDir(catalog.Assets, "assets", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(path, "assets/")
		var dst string
		switch {
		case rel == catalog.ManifestName:
			// The manifest stays in the binary. Copying it out would let a
			// stale file on disk describe services this build cannot install.
			return nil
		case rel == "terraform/pvecli-base.tf" && hasProvider:
			// Two `provider "proxmox"` blocks in one directory is a
			// `terraform init` that fails before doing anything.
			return nil
		case strings.HasPrefix(rel, "terraform/"):
			dst = filepath.Join(terraformDir, strings.TrimPrefix(rel, "terraform/"))
		case strings.HasPrefix(rel, "ansible/"):
			dst = filepath.Join(ansibleDir, strings.TrimPrefix(rel, "ansible/"))
		default:
			return nil
		}

		body, err := catalog.Assets.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.HasPrefix(rel, "terraform/") {
			body = iac.RenderTerraform(body, site)
		}
		state, err := inspectFile(dst, body)
		if err != nil {
			return err
		}
		files = append(files, scaffoldFile{asset: path, dst: dst, body: body, state: state})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(files, func(i, j int) bool { return files[i].dst < files[j].dst })
	return files, nil
}

// inspectFile compares what is on disk with what would be written. Byte
// equality rather than a version stamp, for the reason inspectAgent gives: a
// version number says what a file claimed to be, never whether it was edited.
func inspectFile(path string, want []byte) (fileState, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fileAbsent, nil
	}
	if err != nil {
		return fileAbsent, fmt.Errorf("lecture de %s : %w", path, err)
	}
	if string(raw) == string(want) {
		return fileCurrent, nil
	}
	return fileModified, nil
}

// declaresProxmoxProvider reports whether the directory already configures the
// provider. A lab that grew its own main.tf keeps it; only an empty directory
// gets the base file.
func declaresProxmoxProvider(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("lecture de %s : %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tf") {
			continue
		}
		// pvecli-base.tf is our own; finding it must not make us skip writing it.
		if e.Name() == "pvecli-base.tf" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return false, fmt.Errorf("lecture de %s : %w", e.Name(), err)
		}
		if strings.Contains(string(raw), `provider "proxmox"`) {
			return true, nil
		}
	}
	return false, nil
}

// resolveSite answers the one question the embedded module cannot: which
// gateway, and which resolvers, the guests of THIS lab must use.
//
// Order, and why: the configuration wins, because an operator who typed
// `pvecli config set iac.ipv4_gateway` has already met the case where the
// node's own route is not the guests' route. Failing that, the node is asked —
// it carries the answer in the `gateway` of its bridge, and asking beats
// guessing. Failing that too, nothing: the module renders a null default and
// Terraform refuses, which is the honest outcome. A literal fallback is what
// produced guests routed to an address nobody answers on.
//
// No error is returned. Scaffolding must stay possible with no credential and
// no node reachable — writing the module is a local gesture. What the operator
// gets in that case is a line saying the gateway is unknown, and the command
// to fix it.
func resolveSite(cmd *cobra.Command, eff *config.Effective) iac.Site {
	site := iac.Site{DNSServers: eff.IaC.DNSServers}

	if eff.IaC.IPv4Gateway != "" {
		site.IPv4Gateway, site.Source = eff.IaC.IPv4Gateway, "iac.ipv4_gateway"
		return site
	}
	if eff.Node == "" {
		return site
	}
	client, err := newClient(cmd)
	if err != nil {
		return site
	}
	netcfg, err := client.Network(cmd.Context(), eff.Node, "")
	if err != nil {
		return site
	}
	gw, iface := iac.GatewayFromNetwork(netcfg.Ifaces)
	if gw == "" {
		return site
	}
	site.IPv4Gateway, site.Source = gw, fmt.Sprintf("%s de %s", iface, eff.Node)
	return site
}

// siteLine is what scaffold prints about the network it just wrote in. It names
// the source: « 192.0.2.254 lu sur vmbr0 » is checkable, « 192.0.2.254 » is not.
func siteLine(site iac.Site) string {
	if site.IPv4Gateway == "" {
		return `passerelle du site INCONNUE — « vm_ipv4_gateway » reste à null, et
« terraform plan » refusera de router les invités. Pose-la :

  pvecli config set iac.ipv4_gateway <ip>   puis relance « pvecli iac scaffold »`
	}
	line := fmt.Sprintf("passerelle du site : %s (%s)", site.IPv4Gateway, site.Source)
	if len(site.DNSServers) > 0 {
		return line + fmt.Sprintf(" — résolveurs : %s", strings.Join(site.DNSServers, ", "))
	}
	return line + " — résolveurs : la passerelle"
}

// files2data is what `-o json` renders: the plan, without the file bodies.
func files2data(files []scaffoldFile) any {
	type row struct {
		Path  string `json:"path" yaml:"path"`
		State string `json:"state" yaml:"state"`
	}
	out := make([]row, 0, len(files))
	for _, f := range files {
		out = append(out, row{Path: f.dst, State: f.state.label()})
	}
	return out
}
