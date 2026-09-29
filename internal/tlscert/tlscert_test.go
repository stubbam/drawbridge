package tlscert

import (
	"crypto/tls"
	"crypto/x509"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"
)

var now = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

func TestEnsureCreatesAndKeeps(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls")
	names := DefaultNames("drawbridge-host", netip.MustParseAddr("192.168.4.10"), netip.MustParseAddr("10.8.0.1"))
	cert, created, err := Ensure(dir, names, now)
	if err != nil || !created {
		t.Fatalf("Ensure = created %v, %v", created, err)
	}
	leaf := cert.Leaf
	if leaf == nil {
		t.Fatal("no parsed leaf")
	}
	if want := []string{"localhost", "drawbridge-host", "drawbridge-host.local"}; !slices.Equal(leaf.DNSNames, want) {
		t.Errorf("DNS names %v, want %v", leaf.DNSNames, want)
	}
	var ips []string
	for _, ip := range leaf.IPAddresses {
		ips = append(ips, ip.String())
	}
	if want := []string{"::1", "127.0.0.1", "192.168.4.10", "10.8.0.1"}; !slices.Equal(ips, want) {
		t.Errorf("IP addresses %v, want %v", ips, want)
	}
	if days := leaf.NotAfter.Sub(leaf.NotBefore).Hours() / 24; days > 825 {
		t.Errorf("valid for %.0f days; Apple's platforms reject more than 825", days)
	}
	if !slices.Equal(leaf.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}) || leaf.IsCA {
		t.Errorf("key usage %v, CA %v: want a server certificate", leaf.ExtKeyUsage, leaf.IsCA)
	}
	for file, want := range map[string]os.FileMode{"key.pem": 0o600, "cert.pem": 0o644, ".": 0o700} {
		info, err := os.Stat(filepath.Join(dir, file))
		if err != nil || info.Mode().Perm() != want {
			t.Errorf("%s: mode %v (err %v), want %o", file, info.Mode().Perm(), err, want)
		}
	}

	again, created, err := Ensure(dir, names, now.Add(24*time.Hour))
	if err != nil || created || Fingerprint(again) != Fingerprint(cert) {
		t.Fatalf("second Ensure: created %v, err %v, same %v", created, err, Fingerprint(again) == Fingerprint(cert))
	}

	// Close to expiry, it's replaced.
	renewed, created, err := Ensure(dir, names, leaf.NotAfter.Add(-RenewBefore+time.Hour))
	if err != nil || !created || Fingerprint(renewed) == Fingerprint(cert) {
		t.Fatalf("Ensure near expiry: created %v, err %v", created, err)
	}
}

func TestEnsureReplacesDamagedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), []byte("not a certificate"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, created, err := Ensure(dir, DefaultNames("server"), now); err != nil || !created {
		t.Fatalf("Ensure over a damaged file: created %v, err %v", created, err)
	}
}

func TestHandshake(t *testing.T) {
	cert, _, err := Ensure(t.TempDir(), DefaultNames("server"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", Config(cert))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.(*tls.Conn).Handshake()
			_ = c.Close()
		}
	}()
	pool := x509.NewCertPool()
	pool.AddCert(cert.Leaf)
	conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("a client that trusts the certificate can't connect to 127.0.0.1: %v", err)
	}
	_ = conn.Close()
	if _, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{MinVersion: tls.VersionTLS12}); err == nil {
		t.Fatal("a client that doesn't trust the certificate connected")
	}
}

func TestFingerprint(t *testing.T) {
	cert, _, _ := Ensure(t.TempDir(), DefaultNames("server"), now)
	if fp := Fingerprint(cert); !regexp.MustCompile(`^([0-9A-F]{2}:){31}[0-9A-F]{2}$`).MatchString(fp) {
		t.Fatalf("Fingerprint = %q", fp)
	}
	if Fingerprint(tls.Certificate{}) != "" {
		t.Fatal("an empty certificate has a fingerprint")
	}
}

func TestDefaultNames(t *testing.T) {
	n := DefaultNames("Server.example.com.", netip.MustParseAddr("::ffff:192.168.4.10"), netip.MustParseAddr("127.0.0.1"), netip.Addr{})
	if !slices.Equal(n.DNS, []string{"localhost", "server.example.com"}) {
		t.Errorf("DNS %v", n.DNS)
	}
	if len(n.IPs) != 3 || n.IPs[2] != netip.MustParseAddr("192.168.4.10") {
		t.Errorf("IPs %v", n.IPs)
	}
	if n := DefaultNames("localhost"); len(n.DNS) != 1 {
		t.Errorf("DNS for localhost %v", n.DNS)
	}
}
