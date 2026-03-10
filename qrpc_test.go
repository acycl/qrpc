package qrpc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"testing"

	"github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// generateTLSConfig creates a self-signed TLS certificate for testing.
func generateTLSConfig(tb testing.TB) *tls.Config {
	tb.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		tb.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		DNSNames:     []string{"localhost"},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		tb.Fatal(err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{certDER},
			PrivateKey:  key,
		}},
	}
}

// echoHandler is a MethodHandler that echoes the request payload back.
func echoHandler(srv any, ctx context.Context, payload []byte) (proto.Message, error) {
	in := new(wrapperspb.BytesValue)
	if err := proto.Unmarshal(payload, in); err != nil {
		return nil, err
	}
	return in, nil
}

// setupEnv starts a qrpc server with an echo service and returns a connected
// client. The server and client are cleaned up when the test/benchmark ends.
func setupEnv(tb testing.TB) *ClientConn {
	tb.Helper()

	tlsConfig := generateTLSConfig(tb)

	s := NewServer()
	s.RegisterService(&ServiceDesc{
		ServiceName: "test.Echo",
		Methods: []MethodDesc{
			{MethodName: "Echo", Handler: echoHandler},
		},
	}, nil)

	serverTLS := tlsConfig.Clone()
	serverTLS.NextProtos = []string{alpnProtocol}

	listener, err := quic.ListenAddr("127.0.0.1:0", serverTLS, defaultQUICConfig())
	if err != nil {
		tb.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	tb.Cleanup(cancel)

	go s.Serve(ctx, listener)

	clientTLS := &tls.Config{InsecureSkipVerify: true}
	cc, err := Dial(context.Background(), listener.Addr().String(), clientTLS)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { cc.Close() })

	return cc
}

func TestUnaryRPC(t *testing.T) {
	cc := setupEnv(t)
	ctx := context.Background()

	req := &wrapperspb.BytesValue{Value: []byte("hello qrpc")}
	reply := new(wrapperspb.BytesValue)

	if err := cc.Invoke(ctx, "/test.Echo/Echo", req, reply); err != nil {
		t.Fatal(err)
	}
	if string(reply.Value) != "hello qrpc" {
		t.Fatalf("reply = %q, want %q", reply.Value, "hello qrpc")
	}
}

func TestUnaryRPCUnknownService(t *testing.T) {
	cc := setupEnv(t)
	ctx := context.Background()

	req := &wrapperspb.BytesValue{Value: []byte("test")}
	reply := new(wrapperspb.BytesValue)

	err := cc.Invoke(ctx, "/unknown.Svc/Method", req, reply)
	if err == nil {
		t.Fatal("expected error for unknown service")
	}
}

func TestUnaryRPCUnknownMethod(t *testing.T) {
	cc := setupEnv(t)
	ctx := context.Background()

	req := &wrapperspb.BytesValue{Value: []byte("test")}
	reply := new(wrapperspb.BytesValue)

	err := cc.Invoke(ctx, "/test.Echo/Unknown", req, reply)
	if err == nil {
		t.Fatal("expected error for unknown method")
	}
}

func BenchmarkUnaryRPC(b *testing.B) {
	payloads := map[string][]byte{
		"64B":  make([]byte, 64),
		"1KB":  make([]byte, 1024),
		"64KB": make([]byte, 64*1024),
	}

	cc := setupEnv(b)
	ctx := context.Background()

	for name, payload := range payloads {
		b.Run(name, func(b *testing.B) {
			req := &wrapperspb.BytesValue{Value: payload}
			b.SetBytes(int64(proto.Size(req)) * 2) // request + response
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				reply := new(wrapperspb.BytesValue)
				if err := cc.Invoke(ctx, "/test.Echo/Echo", req, reply); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkUnaryRPCParallel(b *testing.B) {
	payloads := map[string][]byte{
		"64B":  make([]byte, 64),
		"1KB":  make([]byte, 1024),
		"64KB": make([]byte, 64*1024),
	}

	cc := setupEnv(b)
	ctx := context.Background()

	for name, payload := range payloads {
		b.Run(name, func(b *testing.B) {
			req := &wrapperspb.BytesValue{Value: payload}
			b.SetBytes(int64(proto.Size(req)) * 2)
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					reply := new(wrapperspb.BytesValue)
					if err := cc.Invoke(ctx, "/test.Echo/Echo", req, reply); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}
