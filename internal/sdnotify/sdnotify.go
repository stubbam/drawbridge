// Package sdnotify implements the systemd service notification protocol (sd_notify) that
// Type=notify units use to report readiness.
package sdnotify

import (
	"net"
	"os"
)

// Notify sends state, such as "READY=1", to the socket named by $NOTIFY_SOCKET. It does
// nothing when the variable is unset, so the daemon behaves the same outside systemd.
func Notify(state string) error {
	name := os.Getenv("NOTIFY_SOCKET")
	if name == "" {
		return nil
	}
	if name[0] == '@' {
		// A leading "@" names a socket in the abstract namespace.
		name = "\x00" + name[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: name, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte(state))
	return err
}
