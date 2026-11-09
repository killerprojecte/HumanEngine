package app

import (
	"net"
	"sync/atomic"
)

// snowSeedWireListener passively observes the peer's first completed write after
// the seed is held. No server application writes occur during that interval, so
// the test can verify that Ping really reached the wire before releasing ingress.
// This never delays or changes network bytes and is only used by the fixture.
type snowSeedWireListener struct {
	net.Listener
	armed   atomic.Bool
	written chan []byte
}

func (l *snowSeedWireListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &snowSeedWireConn{Conn: c, listener: l}, nil
}

type snowSeedWireConn struct {
	net.Conn
	listener *snowSeedWireListener
}

func (c *snowSeedWireConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if err == nil && n == len(p) && c.listener.armed.CompareAndSwap(true, false) {
		c.listener.written <- append([]byte(nil), p...)
	}
	return n, err
}
