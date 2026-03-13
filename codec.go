// Package qrpc implements a QUIC-based RPC framework compatible with protobuf
// service definitions.
//
// Wire format for requests:
//
//	[4B method_len] [4B payload_len] [method_bytes] [payload_bytes]
//
// Wire format for responses:
//
//	[1B status] [4B payload_len] [payload_bytes]
//
// On success (status=0), payload_bytes is the protobuf-encoded RPC response.
// On error (status=1), payload_bytes is a marshaled statuspb.Status message
// containing the error code, human-readable message, and optional details.
package qrpc

import (
	"encoding/binary"
	"fmt"
	"io"

	"google.golang.org/protobuf/proto"
)

const (
	statusOK    byte = 0
	statusError byte = 1

	// maxMethodSize is the maximum allowed length for a method name (1 KiB).
	maxMethodSize = 1 << 10

	// maxPayloadSize is the maximum allowed size for a single message payload
	// (16 MiB).
	maxPayloadSize = 16 << 20

	// alpnProtocol is the ALPN protocol identifier used during the TLS
	// handshake to negotiate the qrpc protocol.
	alpnProtocol = "qrpc"
)

// marshalRequest marshals req and writes the complete request frame to w in a
// single write call. A pooled buffer is used to avoid per-call heap
// allocations. The method string and protobuf payload are encoded directly into
// one contiguous buffer using proto.MarshalAppend.
func marshalRequest(w io.Writer, method string, req proto.Message) error {
	methodLen := len(method)
	payloadSize := proto.Size(req)

	bp := getBuf(8 + methodLen + payloadSize)
	buf := (*bp)[:8+methodLen]
	binary.BigEndian.PutUint32(buf, uint32(methodLen))
	copy(buf[8:], method)

	buf, err := proto.MarshalOptions{}.MarshalAppend(buf, req)
	if err != nil {
		*bp = buf
		putBuf(bp)
		return fmt.Errorf("qrpc: marshal request: %w", err)
	}
	binary.BigEndian.PutUint32(buf[4:], uint32(len(buf)-8-methodLen))

	_, writeErr := w.Write(buf)
	*bp = buf
	putBuf(bp)
	return writeErr
}

// readRequest reads a request frame from r, returning the method name and raw
// payload as byte slices. Both slices share the backing array of the returned
// poolBuf; the caller must call poolBuf.release when both slices are no longer
// needed.
func readRequest(r io.Reader) (method, payload []byte, buf poolBuf, err error) {
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, nil, poolBuf{}, err
	}

	methodLen := binary.BigEndian.Uint32(hdr[0:])
	payloadLen := binary.BigEndian.Uint32(hdr[4:])
	if methodLen > maxMethodSize {
		return nil, nil, poolBuf{}, fmt.Errorf("qrpc: method name length %d exceeds maximum %d", methodLen, maxMethodSize)
	}
	if payloadLen > maxPayloadSize {
		return nil, nil, poolBuf{}, fmt.Errorf("qrpc: payload size %d exceeds maximum %d", payloadLen, maxPayloadSize)
	}

	total := int(methodLen + payloadLen)
	bp, err := readBuf(r, total)
	if err != nil {
		return nil, nil, poolBuf{}, err
	}
	return (*bp)[:methodLen], (*bp)[methodLen:], poolBuf{bp: bp}, nil
}

// writeErrorResponse converts rpcErr to a *Status (if it isn't one already),
// marshals the status as a protocol buffer, and writes the error response
// frame to w in a single write call. A pooled buffer is used to avoid per-call
// heap allocations.
func writeErrorResponse(w io.Writer, rpcErr error) error {
	st := FromError(rpcErr)
	sp := statusProto(st)
	size := proto.Size(sp)

	bp := getBuf(5 + size)
	buf := (*bp)[:5]
	buf[0] = statusError

	buf, err := proto.MarshalOptions{}.MarshalAppend(buf, sp)
	if err != nil {
		*bp = buf
		putBuf(bp)
		return fmt.Errorf("qrpc: marshal status: %w", err)
	}
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(buf)-5))

	_, writeErr := w.Write(buf)
	*bp = buf
	putBuf(bp)
	return writeErr
}

// readResponse reads a response frame from r. On success it returns the raw
// payload bytes backed by a pooled buffer. The caller must call
// poolBuf.release when the payload is no longer needed. On error status it
// returns a *Status error (no poolBuf to release).
func readResponse(r io.Reader) ([]byte, poolBuf, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, poolBuf{}, err
	}

	flag := hdr[0]
	payloadLen := binary.BigEndian.Uint32(hdr[1:])
	if payloadLen > maxPayloadSize {
		return nil, poolBuf{}, fmt.Errorf("qrpc: payload size %d exceeds maximum %d", payloadLen, maxPayloadSize)
	}

	if payloadLen == 0 {
		if flag == statusError {
			return nil, poolBuf{}, &Status{code: Unknown}
		}
		return nil, poolBuf{}, nil
	}

	bp, err := readBuf(r, int(payloadLen))
	if err != nil {
		return nil, poolBuf{}, err
	}
	if flag == statusError {
		st, unmarshalErr := unmarshalStatus(*bp)
		putBuf(bp)
		if unmarshalErr != nil {
			return nil, poolBuf{}, fmt.Errorf("qrpc: unmarshal status: %w", unmarshalErr)
		}
		return nil, poolBuf{}, st
	}
	return *bp, poolBuf{bp: bp}, nil
}
