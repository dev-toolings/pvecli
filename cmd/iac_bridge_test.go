package cmd

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dev-toolings/pvecli/internal/config"
	"github.com/dev-toolings/pvecli/internal/testutil"
)

// The provider can carry a CA (SSL_CERT_FILE) and can be told to trust
// nothing (--insecure). Everything pvecli verifies beyond that, Terraform
// cannot, and that is exactly when the bridge exists.
func TestTerraformNeedsBridgeOnlyForWhatTheProviderCannotVerify(t *testing.T) {
	cases := []struct {
		name string
		eff  config.Effective
		want bool
	}{
		{"rien de configuré", config.Effective{}, false},
		{"CA seule", config.Effective{TLS: config.TLS{CAFile: "/ca.pem"}}, false},
		{"server_name", config.Effective{TLS: config.TLS{ServerName: "pve.test"}}, true},
		{"empreinte", config.Effective{TLS: config.TLS{Fingerprint: "AA:BB"}}, true},
		{"--insecure gagne", config.Effective{Insecure: true, TLS: config.TLS{ServerName: "pve.test"}}, false},
	}
	for _, c := range cases {
		if got := terraformNeedsBridge(&c.eff); got != c.want {
			t.Errorf("%s : pont=%v, attendu %v", c.name, got, c.want)
		}
	}
}

// A certificate issued for a name only, reached by address: the very case
// tls.server_name exists for. Through the bridge the call is verified and
// succeeds; without the name, the same trust material refuses the address —
// which proves the bridge verifies rather than forwards blindly.
func TestTheBridgeVerifiesTheNameTheAddressDoesNotCarry(t *testing.T) {
	cert, key := selfSignedFor(t, "pve.test")
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "PVEAPIToken=automation@pve!pvectl=s3cr3t" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"version":"9.2.6"}}`)
	}))
	upstream.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{cert.Raw}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	upstream.Config.ErrorLog = log.New(io.Discard, "", 0) // the refused handshake below is the point, not noise
	upstream.StartTLS()
	defer upstream.Close()

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}

	call := func(eff config.Effective) (int, string) {
		t.Helper()
		bridge, err := startTerraformBridge(&eff)
		if err != nil {
			t.Fatalf("démarrage du pont : %v", err)
		}
		defer func() {
			bridge.Close()
			if _, err := os.Stat(bridge.CertFile); !os.IsNotExist(err) {
				t.Errorf("le certificat du pont doit disparaître avec lui : %v", err)
			}
		}()
		if !strings.HasPrefix(bridge.URL, "https://127.0.0.1:") {
			t.Fatalf("le pont doit écouter en TLS sur la boucle locale (le provider refuse http://), pas %s", bridge.URL)
		}
		// Terraform trusts the bridge through SSL_CERT_FILE ; this client does
		// the same by hand.
		pool := x509.NewCertPool()
		bridgePEM, err := os.ReadFile(bridge.CertFile)
		if err != nil || !pool.AppendCertsFromPEM(bridgePEM) {
			t.Fatalf("le certificat du pont doit être lisible en PEM : %v", err)
		}
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
		req, _ := http.NewRequest(http.MethodGet, bridge.URL+"/api2/json/version", nil)
		req.Header.Set("Authorization", "PVEAPIToken=automation@pve!pvectl=s3cr3t")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("appel du pont : %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	// upstream.URL is https://127.0.0.1:<port> : an address, and the
	// certificate names none.
	status, body := call(config.Effective{Endpoint: upstream.URL, TLS: config.TLS{CAFile: caFile, ServerName: "pve.test"}})
	if status != http.StatusOK || !strings.Contains(body, "9.2.6") {
		t.Errorf("avec server_name le relais doit aboutir, vérifié : %d %s", status, body)
	}

	status, body = call(config.Effective{Endpoint: upstream.URL, TLS: config.TLS{CAFile: caFile}})
	if status != http.StatusBadGateway || !strings.Contains(body, "pvecli") {
		t.Errorf("sans le nom, la même CA doit refuser l'adresse — le pont vérifie, il ne relaie pas à l'aveugle : %d %s", status, body)
	}
}

// End to end: with tls.server_name configured, terraform receives the bridge
// as proxmox_endpoint, on the command line so that it wins over
// terraform.tfvars, and before the operator's own arguments.
func TestServerNameSendsTerraformThroughTheBridge(t *testing.T) {
	tfDir := t.TempDir()
	calls := stubTool(t, "terraform", `{"format_version":"1.0"}`)

	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfg, []byte(
		"current_context: test\ncontexts:\n  test:\n    tls:\n      server_name: pve.test\n    iac:\n      terraform_dir: "+tfDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PVECLI_CONFIG", cfg)
	t.Setenv("TF_VAR_proxmox_api_token", "")

	srv := testutil.New(t, "../testdata", map[string]string{
		"GET /api2/json/version": "version.json",
	})
	point(t, srv.URL)

	_, stderr, err := run(t, "iac", "plan", "--node", "pve", "--", "-refresh=false")
	if err != nil {
		t.Fatalf("iac plan : %v", err)
	}
	body, err := os.ReadFile(calls) //nolint:gosec // path built by the test
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if !strings.Contains(got, "argv: plan -var proxmox_endpoint=https://127.0.0.1:") {
		t.Errorf("terraform doit recevoir le pont en -var, avant les arguments de l'opérateur :\n%s", got)
	}
	if !strings.Contains(got, "-refresh=false") {
		t.Errorf("les arguments de l'opérateur doivent suivre :\n%s", got)
	}
	if !strings.Contains(got, "bridge-ca: present") {
		t.Errorf("SSL_CERT_FILE doit désigner le certificat du pont, présent pendant le run :\n%s", got)
	}
	if !strings.Contains(stderr, "tls.server_name pve.test") {
		t.Errorf("stderr doit dire ce qui est vérifié et par quoi Terraform passe :\n%s", stderr)
	}
}

// Without server_name or fingerprint nothing changes for Terraform: the
// endpoint stays whatever the .tf files declare.
func TestWithoutANameTerraformKeepsItsOwnEndpoint(t *testing.T) {
	tfDir := t.TempDir()
	calls := stubTool(t, "terraform", `{"format_version":"1.0"}`)
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfg, []byte(
		"current_context: test\ncontexts:\n  test:\n    iac:\n      terraform_dir: "+tfDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PVECLI_CONFIG", cfg)
	t.Setenv("TF_VAR_proxmox_api_token", "")
	srv := testutil.New(t, "../testdata", map[string]string{"GET /api2/json/version": "version.json"})
	point(t, srv.URL)

	if _, _, err := run(t, "iac", "plan", "--node", "pve"); err != nil {
		t.Fatalf("iac plan : %v", err)
	}
	body, _ := os.ReadFile(calls) //nolint:gosec // path built by the test
	if strings.Contains(string(body), "proxmox_endpoint=") {
		t.Errorf("sans server_name ni empreinte, pas de -var proxmox_endpoint :\n%s", body)
	}
}

func selfSignedFor(t *testing.T, name string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		DNSNames:              []string{name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}
