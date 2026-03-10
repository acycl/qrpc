package qrpc

import (
	"time"

	"github.com/quic-go/quic-go"
)

// defaultQUICConfig returns a quic.Config tuned for RPC workloads. The
// defaults prioritize high concurrency and adequate flow control for typical
// protobuf message sizes.
func defaultQUICConfig() *quic.Config {
	return &quic.Config{
		// Allow a high number of concurrent streams per connection. Each
		// unary RPC opens one stream, so this limits concurrent RPCs.
		MaxIncomingStreams: 1 << 16, // 65536

		// Generous idle timeout with keep-alives to prevent long-running
		// benchmark or production connections from being dropped.
		MaxIdleTimeout:  60 * time.Second,
		KeepAlivePeriod: 15 * time.Second,

		// Per-stream flow control. 1 MiB initial window covers most unary
		// RPC payloads without requiring window updates.
		InitialStreamReceiveWindow: 1 << 20, // 1 MiB
		MaxStreamReceiveWindow:     8 << 20, // 8 MiB

		// Connection-level flow control. The connection window must be at
		// least as large as the per-stream window times the expected
		// concurrent stream count.
		InitialConnectionReceiveWindow: 2 << 20,  // 2 MiB
		MaxConnectionReceiveWindow:     64 << 20, // 64 MiB
	}
}
