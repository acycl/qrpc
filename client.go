package qrpc

import (
	"context"
	"crypto/tls"
	"fmt"

	"github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"
)

// ClientConn represents a QUIC connection to a qrpc server. It is safe for
// concurrent use; each RPC call opens a new QUIC stream.
type ClientConn struct {
	conn quic.Connection
}

// Dial establishes a QUIC connection to the server at addr. The TLS
// configuration must set ServerName (or InsecureSkipVerify for testing). The
// ALPN protocol is set automatically.
func Dial(ctx context.Context, addr string, tlsConfig *tls.Config) (*ClientConn, error) {
	tc := tlsConfig.Clone()
	tc.NextProtos = []string{alpnProtocol}

	conn, err := quic.DialAddr(ctx, addr, tc, defaultQUICConfig())
	if err != nil {
		return nil, fmt.Errorf("qrpc: dial: %w", err)
	}
	return &ClientConn{conn: conn}, nil
}

// Invoke performs a unary RPC call. The method argument must be a fully
// qualified method path of the form "/service.Name/MethodName". The req and
// reply arguments are protobuf messages. The request is marshaled directly into
// a pooled wire frame to minimize allocations. This method is typically called
// by generated client stubs rather than directly by application code.
func (cc *ClientConn) Invoke(ctx context.Context, method string, req, reply proto.Message) error {
	stream, err := cc.conn.OpenStreamSync(ctx)
	if err != nil {
		return fmt.Errorf("qrpc: open stream: %w", err)
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

// Close closes the underlying QUIC connection.
func (cc *ClientConn) Close() error {
	return cc.conn.CloseWithError(0, "client closed")
}
