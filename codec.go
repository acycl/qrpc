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
package qrpc

import (
	"encoding/binary"
	"errors"
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

// marshalResponse marshals msg and writes the complete success response frame
// to w in a single write call. A pooled buffer is used to avoid per-call heap
// allocations.
func marshalResponse(w io.Writer, msg proto.Message) error {
	payloadSize := proto.Size(msg)

	bp := getBuf(5 + payloadSize)
	buf := (*bp)[:5]
	buf[0] = statusOK

	buf, err := proto.MarshalOptions{}.MarshalAppend(buf, msg)
	if err != nil {
		*bp = buf
		putBuf(bp)
		return fmt.Errorf("qrpc: marshal response: %w", err)
	}
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(buf)-5))

	_, writeErr := w.Write(buf)
	*bp = buf
	putBuf(bp)
	return writeErr
}

// writeErrorResponse writes a complete error response frame to w in a single
// write call.
func writeErrorResponse(w io.Writer, rpcErr error) error {
	msg := rpcErr.Error()
	buf := make([]byte, 5+len(msg))
	buf[0] = statusError
	binary.BigEndian.PutUint32(buf[1:], uint32(len(msg)))
	copy(buf[5:], msg)
	_, err := w.Write(buf)
	return err
}

// readResponse reads a response frame from r. On success it returns the raw
// payload bytes backed by a pooled buffer. The caller must call
// poolBuf.release when the payload is no longer needed. On error status it
// returns an error containing the remote message (no poolBuf to release).
func readResponse(r io.Reader) ([]byte, poolBuf, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, poolBuf{}, err
	}

	status := hdr[0]
	payloadLen := binary.BigEndian.Uint32(hdr[1:])
	if payloadLen > maxPayloadSize {
		return nil, poolBuf{}, fmt.Errorf("qrpc: payload size %d exceeds maximum %d", payloadLen, maxPayloadSize)
	}

	if payloadLen == 0 {
		if status == statusError {
			return nil, poolBuf{}, errors.New("qrpc: remote error: (empty)")
		}
		return nil, poolBuf{}, nil
	}

	bp, err := readBuf(r, int(payloadLen))
	if err != nil {
		return nil, poolBuf{}, err
	}
	if status == statusError {
		errMsg := string(*bp)
		putBuf(bp)
		return nil, poolBuf{}, fmt.Errorf("qrpc: remote error: %s", errMsg)
	}
	return *bp, poolBuf{bp: bp}, nil
}
