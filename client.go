package qrpc

import (
	"context"
	"crypto/tls"
	"fmt"
	"sync"

	"github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"
)

// ClientConn represents a QUIC connection to a qrpc server. It is safe for
// concurrent use; each RPC call opens a new QUIC stream. If the underlying
// connection fails, ClientConn transparently reconnects on the next call.
type ClientConn struct {
	addr      string
	tlsConfig *tls.Config

	mu     sync.Mutex
	conn   quic.Connection
	closed bool
}

// Dial establishes a QUIC connection to the server at addr. The TLS
// configuration must set ServerName (or InsecureSkipVerify for testing). The
// ALPN protocol is set automatically. If the connection is later lost,
// subsequent calls to Invoke will attempt to reconnect transparently.
func Dial(ctx context.Context, addr string, tlsConfig *tls.Config) (*ClientConn, error) {
	cc := &ClientConn{
		addr:      addr,
		tlsConfig: tlsConfig.Clone(),
	}
	conn, err := cc.dial(ctx)
	if err != nil {
		return nil, err
	}
	cc.conn = conn
	return cc, nil
}

// dial establishes a new QUIC connection using the stored parameters.
func (cc *ClientConn) dial(ctx context.Context) (quic.Connection, error) {
	tc := cc.tlsConfig.Clone()
	tc.NextProtos = []string{alpnProtocol}
	conn, err := quic.DialAddr(ctx, cc.addr, tc, defaultQUICConfig())
	if err != nil {
		return nil, fmt.Errorf("qrpc: dial: %w", err)
	}
	return conn, nil
}

// openStream returns a new QUIC stream, reconnecting once if the underlying
// connection has failed. Concurrent callers that observe the same failed
// connection coordinate so that only one performs the reconnection.
func (cc *ClientConn) openStream(ctx context.Context) (quic.Stream, error) {
	cc.mu.Lock()
	conn := cc.conn
	cc.mu.Unlock()

	stream, err := conn.OpenStreamSync(ctx)
	if err == nil {
		return stream, nil
	}

	// If the caller's context is done, the failure is not necessarily a
	// connection problem — don't attempt to reconnect.
	if ctx.Err() != nil {
		return nil, fmt.Errorf("qrpc: open stream: %w", err)
	}

	conn, err = cc.reconnect(ctx, conn)
	if err != nil {
		return nil, err
	}

	stream, err = conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, fmt.Errorf("qrpc: open stream: %w", err)
	}
	return stream, nil
}

// reconnect establishes a new connection if failedConn is still the active
// one. If another goroutine has already reconnected, the new connection is
// returned without dialing again.
func (cc *ClientConn) reconnect(ctx context.Context, failedConn quic.Connection) (quic.Connection, error) {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	if cc.closed {
		return nil, fmt.Errorf("qrpc: connection closed")
	}
	if cc.conn != failedConn {
		return cc.conn, nil
	}

	conn, err := cc.dial(ctx)
	if err != nil {
		return nil, err
	}
	cc.conn = conn
	return conn, nil
}

// Invoke performs a unary RPC call. The method argument must be a fully
// qualified method path of the form "/service.Name/MethodName". The req and
// reply arguments are protobuf messages. The request is marshaled directly into
// a pooled wire frame to minimize allocations. If the connection has failed,
// Invoke reconnects transparently before retrying. This method is typically
// called by generated client stubs rather than directly by application code.
func (cc *ClientConn) Invoke(ctx context.Context, method string, req, reply proto.Message) error {
	stream, err := cc.openStream(ctx)
	if err != nil {
		return err
	}

	// Cancel the stream if the caller's context is cancellable. A nil Done
	// channel (e.g. context.Background) means the context can never be
	// cancelled, so we skip the registration to avoid a closure allocation.
	if ctx.Done() != nil {
		stop := context.AfterFunc(ctx, func() {
			stream.CancelRead(1)
			stream.CancelWrite(1)
		})
		defer stop()
	}

	if err := marshalRequest(stream, method, req); err != nil {
		stream.CancelWrite(1)
		stream.CancelRead(1)
		return err
	}

	// Close the write side so the server sees the end of the request. The
	// read side remains open for receiving the response.
	if err := stream.Close(); err != nil {
		stream.CancelRead(1)
		return fmt.Errorf("qrpc: close write: %w", err)
	}

	respBytes, buf, err := readResponse(stream)
	if err != nil {
		return err
	}

	err = proto.Unmarshal(respBytes, reply)
	buf.release()
	if err != nil {
		return fmt.Errorf("qrpc: unmarshal response: %w", err)
	}
	return nil
}

// Close closes the underlying QUIC connection. Any in-flight RPCs will fail.
// After Close returns, Invoke will return an error without attempting to
// reconnect.
func (cc *ClientConn) Close() error {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	cc.closed = true
	return cc.conn.CloseWithError(0, "client closed")
}
