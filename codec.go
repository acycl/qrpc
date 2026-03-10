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
// single write call. The method string and protobuf payload are encoded
// directly into one contiguous buffer using proto.MarshalAppend.
func marshalRequest(w io.Writer, method string, req proto.Message) error {
	methodLen := len(method)
	payloadSize := proto.Size(req)

	buf := make([]byte, 8+methodLen, 8+methodLen+payloadSize)
	binary.BigEndian.PutUint32(buf[0:], uint32(methodLen))
	// buf[4:8] is a placeholder for payload length, filled after marshal.
	copy(buf[8:], method)

	var err error
	buf, err = proto.MarshalOptions{}.MarshalAppend(buf, req)
	if err != nil {
		return fmt.Errorf("qrpc: marshal request: %w", err)
	}
	binary.BigEndian.PutUint32(buf[4:8], uint32(len(buf)-8-methodLen))

	_, err = w.Write(buf)
	return err
}

// readRequest reads a request frame from r, returning the method name as a
// byte slice (to avoid a string allocation) and the raw payload bytes. The
// returned slices share the same underlying array.
func readRequest(r io.Reader) (method, payload []byte, err error) {
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, nil, err
	}

	methodLen := binary.BigEndian.Uint32(hdr[0:])
	payloadLen := binary.BigEndian.Uint32(hdr[4:])
	if methodLen > maxMethodSize {
		return nil, nil, fmt.Errorf("qrpc: method name length %d exceeds maximum %d", methodLen, maxMethodSize)
	}
	if payloadLen > maxPayloadSize {
		return nil, nil, fmt.Errorf("qrpc: payload size %d exceeds maximum %d", payloadLen, maxPayloadSize)
	}

	buf := make([]byte, methodLen+payloadLen)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, nil, err
	}
	return buf[:methodLen], buf[methodLen:], nil
}

// marshalResponse marshals msg and writes the complete success response frame
// to w in a single write call using proto.MarshalAppend.
func marshalResponse(w io.Writer, msg proto.Message) error {
	payloadSize := proto.Size(msg)

	buf := make([]byte, 5, 5+payloadSize)
	buf[0] = statusOK
	// buf[1:5] is a placeholder for payload length, filled after marshal.

	var err error
	buf, err = proto.MarshalOptions{}.MarshalAppend(buf, msg)
	if err != nil {
		return fmt.Errorf("qrpc: marshal response: %w", err)
	}
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(buf)-5))

	_, err = w.Write(buf)
	return err
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
// payload bytes. On error status it returns an error containing the remote
// message.
func readResponse(r io.Reader) ([]byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}

	status := hdr[0]
	payloadLen := binary.BigEndian.Uint32(hdr[1:])
	if payloadLen > maxPayloadSize {
		return nil, fmt.Errorf("qrpc: payload size %d exceeds maximum %d", payloadLen, maxPayloadSize)
	}

	if payloadLen == 0 {
		if status == statusError {
			return nil, errors.New("qrpc: remote error: (empty)")
		}
		return nil, nil
	}

	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	if status == statusError {
		return nil, fmt.Errorf("qrpc: remote error: %s", payload)
	}
	return payload, nil
}
