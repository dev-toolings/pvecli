package cmd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dev-toolings/pvecli/internal/config"
	"github.com/dev-toolings/pvecli/internal/pve"
)

// The bpg/proxmox provider verifies the node's certificate the way Go does by
// default: against the name written in the endpoint URL. It offers `insecure`
// and honours SSL_CERT_FILE, and that is the whole of its trust vocabulary — no
// server name, no pinned fingerprint. A node reached at 192.168.1.23 that
// presents a certificate issued for pve.example.ts.net therefore fails every
// `terraform plan` with « doesn't contain any IP SANs », while pvecli itself
// talks to the same node, verified, thanks to tls.server_name.
//
// Handing the provider `insecure = true` would make the whole pipeline trust
// nothing to keep one tool happy. Instead pvecli opens a listener on the
// loopback interface for the duration of the command, verifies the node there
// exactly as it does for its own calls, and points Terraform at the listener.
//
// The provider refuses a plain http:// endpoint, so the listener speaks TLS
// too, with a certificate minted for 127.0.0.1 at start-up and forgotten at
// exit. Terraform trusts it through SSL_CERT_FILE — the same hook that already
// carries an operator CA to the provider — and the private key never touches
// the disk. Two verified hops, none of them blind.
type terraformBridge struct {
	// URL is what Terraform is given as proxmox_endpoint.
	URL string
	// CertFile holds the listener's certificate, for SSL_CERT_FILE.
	CertFile string
	target   *url.URL
	srv      *http.Server
	dir      string
}

// terraformNeedsBridge says when Terraform cannot reproduce pvecli's
// verification: a server name to check instead of the address, or a pinned
// fingerprint. A CA file alone needs no bridge (SSL_CERT_FILE carries it), and
// --insecure is the operator's explicit choice, applied as-is.
func terraformNeedsBridge(eff *config.Effective) bool {
	return !eff.Insecure && (eff.TLS.ServerName != "" || eff.TLS.Fingerprint != "")
}

// startTerraformBridge listens on an ephemeral loopback port and relays every
// request to the endpoint with pvecli's own trust settings. Close stops it.
func startTerraformBridge(eff *config.Effective) (*terraformBridge, error) {
	target, err := url.Parse(eff.Endpoint)
	if err != nil || target.Host == "" {
		return nil, fmt.Errorf("endpoint %q illisible", eff.Endpoint)
	}
	tc, err := pve.ClientTLSConfig(pve.TrustOptions{
		Fingerprint: eff.TLS.Fingerprint,
		CAFile:      eff.TLS.CAFile,
		ServerName:  eff.TLS.ServerName,
		Insecure:    eff.Insecure,
	}, target.Host)
	if err != nil {
		return nil, err
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			// SetURL also sets the outgoing Host header to the endpoint's, so
			// the node sees the request it would have seen from Terraform.
			r.SetURL(target)
		},
		Transport: &http.Transport{
			Proxy:             http.ProxyFromEnvironment,
			TLSClientConfig:   tc,
			ForceAttemptHTTP2: true,
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			// A verification failure surfaces here, as a 502 whose body names
			// the cause. Terraform shows the body; a bare 502 would send the
			// operator looking at the node instead of at the certificate.
			http.Error(w, "pvecli : relais vers "+target.Host+" refusé : "+err.Error(), http.StatusBadGateway)
		},
	}

	cert, certPEM, err := loopbackCertificate()
	if err != nil {
		return nil, fmt.Errorf("certificat du relais local pour terraform : %w", err)
	}
	dir, err := os.MkdirTemp("", "pvecli-terraform-bridge-")
	if err != nil {
		return nil, fmt.Errorf("dossier du relais local pour terraform : %w", err)
	}
	certFile := filepath.Join(dir, "bridge-ca.pem")
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("écriture de %s : %w", certFile, err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("ouverture du relais local pour terraform : %w", err)
	}
	srv := &http.Server{
		Handler:           proxy,
		ReadHeaderTimeout: 30 * time.Second,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
	}
	go func() { _ = srv.ServeTLS(ln, "", "") }()

	return &terraformBridge{
		URL:      "https://" + ln.Addr().String(),
		CertFile: certFile,
		target:   target,
		srv:      srv,
		dir:      dir,
	}, nil
}

// loopbackCertificate mints a self-signed certificate for 127.0.0.1, valid for
// the hour a Terraform run may last, and returns it with its PEM form.
func loopbackCertificate() (tls.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "pvecli terraform bridge"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, certPEM, nil
}

// Explain is the stderr notice: what is verified, and what Terraform was told.
func (b *terraformBridge) Explain(eff *config.Effective) string {
	how := "tls.server_name " + eff.TLS.ServerName
	if eff.TLS.Fingerprint != "" {
		how = "l'empreinte épinglée"
	}
	return fmt.Sprintf("TLS : le provider bpg/proxmox ne sait pas vérifier %s.\n"+
		"  pvecli vérifie lui-même le certificat de %s et relaie Terraform par\n"+
		"  %s (boucle locale, certificat éphémère, fermée à la fin de la commande) :\n"+
		"  -var proxmox_endpoint=%s   SSL_CERT_FILE=%s\n\n", how, b.target.Host, b.URL, b.URL, b.CertFile)
}

// Env returns the process environment with SSL_CERT_FILE pointing at the
// listener's certificate: Terraform only ever talks to the bridge, so the
// operator's CA, if any, is the bridge's business, not the provider's.
func (b *terraformBridge) Env(env []string) []string {
	kept := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, "SSL_CERT_FILE=") {
			kept = append(kept, kv)
		}
	}
	return append(kept, "SSL_CERT_FILE="+b.CertFile)
}

// Close stops the listener and forgets the certificate. Terraform has exited
// by then; anything still in flight has two seconds, more than a plan needs.
func (b *terraformBridge) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = b.srv.Shutdown(ctx)
	_ = os.RemoveAll(b.dir)
}
