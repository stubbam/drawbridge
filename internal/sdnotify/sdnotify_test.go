package sdnotify

import (
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestNotifyWithoutSocketIsNoOp(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	if err := Notify("READY=1"); err != nil {
		t.Fatalf("Notify with no socket: %v", err)
	}
}

func TestNotifySendsState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	t.Setenv("NOTIFY_SOCKET", path)

	if err := Notify("READY=1"); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	buf := make([]byte, 64)
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("reading notification: %v", err)
	}
	if got := string(buf[:n]); got != "READY=1" {
		t.Fatalf("got %q, want %q", got, "READY=1")
	}
}

func TestNotifyReportsMissingSocket(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", filepath.Join(t.TempDir(), "missing.sock"))
	if err := Notify("READY=1"); err == nil {
		t.Fatal("Notify to a missing socket succeeded; want an error")
	}
}
