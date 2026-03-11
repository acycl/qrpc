# qrpc

A lightweight RPC framework for Go that runs over [QUIC](https://www.rfc-editor.org/rfc/rfc9000.html) instead of TCP. It uses [Protocol Buffers](https://protobuf.dev/) for service definitions and includes a `protoc` plugin that generates type-safe client and server stubs in the same style as [gRPC-Go](https://pkg.go.dev/google.golang.org/grpc) — server interfaces, registration functions, and typed client constructors.

Each RPC maps to a single QUIC stream, giving you built-in multiplexing, head-of-line blocking elimination, and TLS 1.3 encryption with zero extra setup.

## Features

- **QUIC transport** — multiplexed streams over a single connection with no head-of-line blocking
- **Protobuf code generation** — `protoc-gen-qrpc` generates familiar gRPC-style client interfaces, server interfaces, and registration glue from `.proto` files
- **Transparent reconnection** — `ClientConn` automatically re-establishes the underlying QUIC connection on failure
- **Minimal wire format** — compact binary framing with no HTTP/2 dependency
- **Buffer pooling** — `sync.Pool`-backed buffers minimize per-RPC heap allocations
- **Context cancellation** — client-side context cancellation propagates to stream teardown

## Installation

```sh
go get github.com/acycl/qrpc
```

Install the protoc plugin:

```sh
go install github.com/acycl/qrpc/cmd/protoc-gen-qrpc@latest
```

Or add it as a [tool dependency](https://go.dev/blog/tools) in your `go.mod`:

```sh
go get -tool github.com/acycl/qrpc/cmd/protoc-gen-qrpc
```

This lets you invoke it via `go tool protoc-gen-qrpc` without a global install, which is especially useful with [Buf](#using-buf).

## Quick start

### 1. Define a service

```protobuf
// greeter.proto
syntax = "proto3";
package greeter;
option go_package = "example.com/greeter";

message HelloRequest {
  string name = 1;
}

message HelloReply {
  string message = 1;
}

service Greeter {
  rpc SayHello (HelloRequest) returns (HelloReply);
}
```

### 2. Generate Go code

```sh
protoc --go_out=. --go_opt=paths=source_relative \
       --qrpc_out=. --qrpc_opt=paths=source_relative \
       greeter.proto
```

This produces `greeter.pb.go` (protobuf messages) and `greeter_qrpc.pb.go` (client/server stubs).

The generated code follows the same pattern as gRPC-Go:

- A **server interface** (`GreeterServer`) that you implement
- A **registration function** (`RegisterGreeterServer`) to wire it up
- A **client interface** (`GreeterClient`) with a constructor (`NewGreeterClient`) backed by a `*qrpc.ClientConn`

### 3. Implement the server

```go
package main

import (
	"context"
	"crypto/tls"
	"log"

	"github.com/acycl/qrpc"
	pb "example.com/greeter"
)

type greeterServer struct{}

func (s *greeterServer) SayHello(ctx context.Context, req *pb.HelloRequest) (*pb.HelloReply, error) {
	return &pb.HelloReply{Message: "Hello, " + req.Name + "!"}, nil
}

func main() {
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{mustLoadCert()},
	}

	srv := qrpc.NewServer()
	pb.RegisterGreeterServer(srv, &greeterServer{})

	ctx := context.Background()
	if err := srv.ListenAndServe(ctx, "127.0.0.1:4242", tlsConfig); err != nil {
		log.Fatal(err)
	}
}
```

### 4. Write a client

```go
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"

	"github.com/acycl/qrpc"
	pb "example.com/greeter"
)

func main() {
	tlsConfig := &tls.Config{
		ServerName: "localhost",
		// RootCAs: ...,
	}

	conn, err := qrpc.Dial(context.Background(), "127.0.0.1:4242", tlsConfig)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	client := pb.NewGreeterClient(conn)
	reply, err := client.SayHello(context.Background(), &pb.HelloRequest{Name: "World"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(reply.Message) // Hello, World!
}
```

## Using Buf

If you use [Buf](https://buf.build/) for protobuf management, you can invoke `protoc-gen-qrpc` via `go tool` so that your project's pinned version is always used. Add it as a tool dependency (see [Installation](#installation)), then configure `buf.gen.yaml`:

```yaml
version: v2
plugins:
  - protoc_path: ["go", "tool", "protoc-gen-go"]
    out: .
    opt: paths=source_relative
  - protoc_path: ["go", "tool", "protoc-gen-qrpc"]
    out: .
    opt: paths=source_relative
```

Then generate with:

```sh
buf generate
```

## Wire format

Requests and responses use a compact binary framing. No HTTP/2, no HPACK, no grpc-status trailers — just length-prefixed fields on a QUIC stream.

**Request frame:**

```
[4 bytes: method length] [4 bytes: payload length] [method] [payload]
```

**Response frame (success):**

```
[1 byte: 0x00] [4 bytes: payload length] [payload]
```

**Response frame (error):**

```
[1 byte: 0x01] [4 bytes: message length] [error message]
```

## API overview

### Server

| Function / Method | Description |
|---|---|
| `qrpc.NewServer()` | Create a new server instance |
| `srv.RegisterService(desc)` | Register a service (called by generated code) |
| `srv.HandlerTimeout` | Per-RPC timeout; zero means no limit |
| `srv.ListenAndServe(ctx, addr, tlsConfig)` | Listen on an address and serve RPCs |
| `srv.Serve(ctx, listener)` | Serve RPCs on an existing `quic.Listener` |

### Client

| Function / Method | Description |
|---|---|
| `qrpc.Dial(ctx, addr, tlsConfig)` | Connect to a qrpc server |
| `cc.Invoke(ctx, method, req, reply)` | Make a unary RPC call (called by generated code) |
| `cc.Close()` | Close the connection |

The client transparently reconnects if the underlying QUIC connection is lost. Concurrent callers coordinate so that only one reconnection attempt is made.

### Code generation

The generated code provides typed wrappers so you don't call `Invoke` directly:

- **`RegisterXxxServer(srv, impl)`** — registers your implementation with the server
- **`NewXxxClient(cc)`** — returns a typed client backed by a `*qrpc.ClientConn`

## QUIC configuration

The default QUIC configuration is tuned for RPC workloads:

| Parameter | Value |
|---|---|
| Max concurrent streams | 65,536 |
| Idle timeout | 60 s |
| Keep-alive period | 15 s |
| Initial stream receive window | 1 MiB |
| Max stream receive window | 8 MiB |
| Max connection receive window | 64 MiB |
| Max payload size | 16 MiB |

## Running tests

```sh
go test ./...
```

Run benchmarks:

```sh
go test -bench=. -benchmem ./...
```

## License

See [LICENSE](LICENSE) for details.
