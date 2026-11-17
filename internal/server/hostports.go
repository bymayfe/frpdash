package server

import (
	"net"
	"strconv"
)

// hostPorts tells whether another program on the VDS already listens on a
// port, by trying to bind it for a moment. It runs before a public service
// is created, while frps does not use the port yet; ports of existing
// services are checked against the database instead.
type hostPorts struct{}

// InUse implements fleet.HostPorts.
func (hostPorts) InUse(proto string, port int) bool {
	addr := ":" + strconv.Itoa(port)
	if proto == "udp" {
		c, err := net.ListenPacket("udp", addr)
		if err != nil {
			return true
		}
		_ = c.Close()
		return false
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return true
	}
	_ = l.Close()
	return false
}
