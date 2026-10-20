//go:build linux

package adapterd

import (
	"net"

	"golang.org/x/sys/unix"
)

func peerUID(conn *net.UnixConn) (int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid int
	var callErr error
	err = raw.Control(func(fd uintptr) {
		ucred, ge := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if ge != nil {
			callErr = ge
			return
		}
		uid = int(ucred.Uid)
	})
	if err != nil {
		return 0, err
	}
	return uid, callErr
}

func uidFromConn(conn net.Conn) (int, bool) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	uid, err := peerUID(uc)
	if err != nil {
		return 0, false
	}
	return uid, true
}
