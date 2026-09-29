// Package tlscert makes and keeps the web UI's self-signed certificate (docs/PLAN.md
// §6.6). The UI is only reachable from the LAN and the VPN, so no public certificate
// authority can issue one for it; browsers warn once, and the fingerprint the daemon logs
// lets the admin check that the warning is about this certificate.
package tlscert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	// Validity is how long a new certificate lasts. Apple's platforms reject server
	// certificates valid for more than 825 days, even ones the user trusts by hand.
	Validity = 800 * 24 * time.Hour
	// RenewBefore replaces a certificate this long before it expires.
	RenewBefore = 30 * 24 * time.Hour

	certFile = "cert.pem"
	keyFile  = "key.pem"
)

// Names are what the certificate is valid for.
type Names struct {
	DNS []string
	IPs []netip.Addr
}

// DefaultNames are the names people use to reach this machine: its hostname (with and
// without .local, for mDNS), localhost, loopback, and the given addresses.
func DefaultNames(hostname string, addrs ...netip.Addr) Names {
	n := Names{DNS: []string{"localhost"}, IPs: []netip.Addr{netip.IPv6Loopback(), netip.MustParseAddr("127.0.0.1")}}
	if h := strings.ToLower(strings.TrimSuffix(hostname, ".")); h != "" && h != "localhost" {
		n.DNS = append(n.DNS, h)
		if !strings.Contains(h, ".") {
			n.DNS = append(n.DNS, h+".local")
		}
	}
	for _, a := range addrs {
		if a.IsValid() && !slices.Contains(n.IPs, a.Unmap()) {
			n.IPs = append(n.IPs, a.Unmap())
		}
	}
	return n
}

// Ensure returns the certificate in dir, first creating it if it's missing, unreadable,
// or expiring within RenewBefore. It reports whether it created one. dir is created
// with mode 0700, and the private key is written with mode 0600.
func Ensure(dir string, names Names, now time.Time) (tls.Certificate, bool, error) {
	certPath, keyPath := filepath.Join(dir, certFile), filepath.Join(dir, keyFile)
	// A missing or damaged pair is replaced: the certificate is self-signed, so nothing
	// else depends on it.
	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil && cert.Leaf != nil &&
		now.Before(cert.Leaf.NotAfter.Add(-RenewBefore)) {
		return cert, false, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, false, fmt.Errorf("creating the TLS directory: %w", err)
	}
	certPEM, keyPEM, err := generate(names, now)
	if err != nil {
		return tls.Certificate{}, false, err
	}
	// The key first, so a crash can't leave a new certificate beside an old key.
	if err := writeFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, false, err
	}
	if err := writeFile(certPath, certPEM, 0o644); err != nil {
		return tls.Certificate{}, false, err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	return cert, true, err
}

// writeFile replaces path atomically.
func writeFile(path string, data []byte, mode fs.FileMode) error {
	tmp := path + ".new"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	return nil
}

func generate(names Names, now time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	cn := "Drawbridge"
	if len(names.DNS) > 1 {
		cn = names.DNS[1] // the hostname; [0] is localhost
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn, Organization: []string{"Drawbridge (self-signed)"}},
		// A little slack for clocks that disagree.
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(Validity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              names.DNS,
	}
	for _, a := range names.IPs {
		tmpl.IPAddresses = append(tmpl.IPAddresses, net.IP(a.AsSlice()))
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("creating the TLS certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

// Fingerprint returns the certificate's SHA-256 fingerprint as browsers show it:
// uppercase hex pairs separated by colons.
func Fingerprint(cert tls.Certificate) string {
	if len(cert.Certificate) == 0 {
		return ""
	}
	sum := sha256.Sum256(cert.Certificate[0])
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

// Config returns the TLS configuration for the web UI.
func Config(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
}
