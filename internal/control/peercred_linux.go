package control

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerUID returns the user ID of the process on the other end of a Unix socket.
func peerUID(c net.Conn) (uint32, bool) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var (
		cred    *unix.Ucred
		credErr error
	)
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) //nolint:gosec // G115: a file descriptor fits in an int.
	}); err != nil || credErr != nil {
		return 0, false
	}
	return cred.Uid, true
}
