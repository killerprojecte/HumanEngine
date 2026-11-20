package pinnedhop

import (
	"context"
	"crypto/tls"
	"net"
	"sync"
)

// Open connects to a literal address already admitted by the caller, using
// hostname only for TLS identity. Stop closes I/O; finish also joins the
// cancellation watcher. No HTTP request, proxy, pooling or DNS lookup is owned here.
func Open(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error), tlsConfig *tls.Config,
	addressFamily int, address, port, hostname string, useTLS bool,
) (net.Conn, func(), func(), error) {
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	family := "tcp6"
	if addressFamily == 4 {
		family = "tcp4"
	}
	raw, e := dial(ctx, family, net.JoinHostPort(address, port))
	if e != nil {
		return nil, nil, nil, e
	}
	var conn net.Conn = raw
	stopped, joined := make(chan struct{}), make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(stopped); raw.Close() }) }
	finish := func() { stop(); <-joined }
	go func() {
		defer close(joined)
		select {
		case <-ctx.Done():
			raw.Close()
		case <-stopped:
		}
	}()
	if useTLS {
		config := &tls.Config{}
		if tlsConfig != nil {
			config = tlsConfig.Clone()
		}
		config.ServerName = hostname
		config.NextProtos = []string{"http/1.1"}
		secure := tls.Client(raw, config)
		if e = secure.HandshakeContext(ctx); e != nil {
			finish()
			return nil, nil, nil, e
		}
		conn = secure
	}
	return conn, stop, finish, nil
}
