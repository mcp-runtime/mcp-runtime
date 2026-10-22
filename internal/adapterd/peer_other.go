//go:build !darwin && !linux

package adapterd

import (
	"fmt"
	"net"
)

func peerUID(*net.UnixConn) (int, error) {
	return 0, fmt.Errorf("unix peer credentials are unavailable on this operating system")
}

func uidFromConn(net.Conn) (int, bool) { return 0, false }
