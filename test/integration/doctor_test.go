//go:build integration

package integration

import (
	"errors"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

var doctorLine = regexp.MustCompile(`(?m)^(PASS|WARN|FAIL|SKIP)  (.+)$`)

// doctor runs `drawbridge doctor` against the daemon and returns each check's status by
// name, the full output, and the exit status.
func (s *server) doctor() (map[string]string, string, int) {
	s.t.Helper()
	out, err := s.cliErr("doctor")
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			s.t.Fatalf("drawbridge doctor: %v\n%s", err, out)
		}
		code = ee.ExitCode()
	}
	statuses := map[string]string{}
	for _, m := range doctorLine.FindAllStringSubmatch(out, -1) {
		statuses[m[2]] = m[1]
	}
	if len(statuses) == 0 {
		s.t.Fatalf("drawbridge doctor printed no checks (exit %d):\n%s", code, out)
	}
	return statuses, out, code
}

// flatten joins a wrapped output into one line, so a phrase can be found wherever it wrapped.
func flatten(s string) string { return strings.Join(strings.Fields(s), " ") }

func (s *server) sh(script string) {
	s.t.Helper()
	run(s.t, "ip", "netns", "exec", s.tp.srv, "sh", "-c", script)
}

// The doctor reads the host it runs on: the kernel's sysctls, its routing table, and the
// ruleset nft reports. Here the host is the server namespace, so each fault the doctor
// should find is made in the kernel, and the check must follow it.
func TestDoctorFollowsTheKernel(t *testing.T) {
	tp := newTopology(t)
	srv := newServer(t, tp)
	srv.tunnel("up")
	srv.startDaemon()
	srv.cli("server", "set", "--endpoint", srvAddr4)

	// The uplink relies on kernel SLAAC and forwarding is on: the quiet IPv6 failure the
	// accept_ra check exists for. The topology already turned forwarding on.
	srv.sh("echo 1 >/proc/sys/net/ipv6/conf/srv0/accept_ra")
	st, out, code := srv.doctor()
	for name, want := range map[string]string{
		"Tunnel":                      "PASS",
		"Forwarding sysctls":          "PASS",
		"Uplink":                      "PASS",
		"Router advertisements":       "FAIL",
		"Drawbridge's firewall table": "PASS",
		"Host firewall":               "PASS",
		"TLS certificate":             "PASS",
	} {
		if st[name] != want {
			t.Errorf("%s: %s, want %s\n%s", name, st[name], want, out)
		}
	}
	if flat := flatten(out); code != 1 || !strings.Contains(flat, "accept_ra = 2") || !strings.Contains(flat, "1 failed") {
		t.Errorf("exit %d; want 1 with the accept_ra fix and one failure:\n%s", code, out)
	}

	srv.sh("echo 2 >/proc/sys/net/ipv6/conf/srv0/accept_ra")
	if st, out, _ := srv.doctor(); st["Router advertisements"] != "PASS" {
		t.Errorf("accept_ra=2 didn't pass the check:\n%s", out)
	}

	// IPv4 forwarding off: clients connect and reach nothing.
	srv.sh("echo 0 >/proc/sys/net/ipv4/ip_forward")
	st, out, code = srv.doctor()
	if st["Forwarding sysctls"] != "FAIL" || code != 1 || !strings.Contains(flatten(out), "IPv4 forwarding is off") {
		t.Errorf("with ip_forward off, exit %d:\n%s", code, out)
	}
	srv.sh("echo 1 >/proc/sys/net/ipv4/ip_forward")

	// The same for IPv6, which is the one that fails quietly.
	srv.sh("echo 0 >/proc/sys/net/ipv6/conf/all/forwarding")
	st, out, code = srv.doctor()
	if st["Forwarding sysctls"] != "FAIL" || code != 1 || !strings.Contains(flatten(out), "IPv6 forwarding is off") {
		t.Errorf("with IPv6 forwarding off, exit %d:\n%s", code, out)
	}
	srv.sh("echo 1 >/proc/sys/net/ipv6/conf/all/forwarding")

	// Another firewall's forward chain drops by default and nothing accepts wg0: the
	// ufw, firewalld, and rootful Docker case. This is real nft output, not a fixture.
	srv.sh(`nft add table inet other && nft add chain inet other gate '{ type filter hook forward priority 0 ; policy drop ; }'`)
	st, out, _ = srv.doctor()
	if st["Host firewall"] != "WARN" || !strings.Contains(flatten(out), "the inet other gate chain") {
		t.Errorf("a default-drop forward chain wasn't found:\n%s", out)
	}
	// Accepting wg0's traffic in both directions clears it.
	srv.sh(`nft add rule inet other gate iifname wg0 accept && nft add rule inet other gate oifname wg0 accept`)
	if st, out, _ := srv.doctor(); st["Host firewall"] != "PASS" {
		t.Errorf("accepting wg0 didn't clear the host firewall check:\n%s", out)
	}
	// A rule that accepts only some of wg0's traffic (say, one destination port) isn't a way through.
	srv.sh(`nft flush chain inet other gate && nft add rule inet other gate iifname wg0 tcp dport 443 accept`)
	if st, out, _ := srv.doctor(); st["Host firewall"] != "WARN" {
		t.Errorf("a partial accept rule cleared the host firewall check:\n%s", out)
	}
	srv.sh(`nft delete table inet other`)

	// Tunnel down: the daemon keeps running, and the doctor says the tunnel is stopped
	// and how to start it. The firewall table goes with the tunnel, so that check has
	// nothing to say.
	srv.tunnel("down")
	st, out, code = srv.doctor()
	if st["Tunnel"] != "FAIL" || st["Drawbridge's firewall table"] != "SKIP" || code != 1 ||
		!strings.Contains(out, "systemctl start drawbridge-tunnel") {
		t.Errorf("after tunnel down, exit %d:\n%s", code, out)
	}
}
