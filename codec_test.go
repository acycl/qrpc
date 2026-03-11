package qrpc

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestRoundTripRequest(t *testing.T) {
	method := "/test.Echo/Echo"
	req := &wrapperspb.BytesValue{Value: []byte("hello")}

	var buf bytes.Buffer
	if err := marshalRequest(&buf, method, req); err != nil {
		t.Fatal(err)
	}

	gotMethod, gotPayload, pb, err := readRequest(&buf)
	if err != nil {
		t.Fatal(err)
	}
	defer pb.release()

	if string(gotMethod) != method {
		t.Fatalf("method = %q, want %q", gotMethod, method)
	}

	got := new(wrapperspb.BytesValue)
	if err := proto.Unmarshal(gotPayload, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Value, req.Value) {
		t.Fatalf("payload = %q, want %q", got.Value, req.Value)
	}
}

func TestRoundTripResponse(t *testing.T) {
	resp := &wrapperspb.BytesValue{Value: []byte("world")}
	respBytes, err := proto.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := writeResponse(&buf, respBytes); err != nil {
		t.Fatal(err)
	}

	gotPayload, pb, err := readResponse(&buf)
	if err != nil {
		t.Fatal(err)
	}
	defer pb.release()

	got := new(wrapperspb.BytesValue)
	if err := proto.Unmarshal(gotPayload, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Value, resp.Value) {
		t.Fatalf("payload = %q, want %q", got.Value, resp.Value)
	}
}

func TestRoundTripErrorResponse(t *testing.T) {
	var buf bytes.Buffer
	writeErrorResponse(&buf, errors.New("something broke"))

	_, _, err := readResponse(&buf)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); got != "qrpc: remote error: something broke" {
		t.Fatalf("error = %q, want %q", got, "qrpc: remote error: something broke")
	}
}

var benchPayloads = map[string][]byte{
	"64B":  make([]byte, 64),
	"1KB":  make([]byte, 1024),
	"64KB": make([]byte, 64*1024),
}

func BenchmarkMarshalRequest(b *testing.B) {
	for name, payload := range benchPayloads {
		b.Run(name, func(b *testing.B) {
			req := &wrapperspb.BytesValue{Value: payload}
			method := "/test.Echo/Echo"
			b.SetBytes(int64(8 + len(method) + proto.Size(req)))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				marshalRequest(io.Discard, method, req)
			}
		})
	}
}

func BenchmarkReadRequest(b *testing.B) {
	for name, payload := range benchPayloads {
		b.Run(name, func(b *testing.B) {
			req := &wrapperspb.BytesValue{Value: payload}
			var buf bytes.Buffer
			marshalRequest(&buf, "/test.Echo/Echo", req)
			data := buf.Bytes()
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				_, _, pb, _ := readRequest(bytes.NewReader(data))
				pb.release()
			}
		})
	}
}

func BenchmarkWriteResponse(b *testing.B) {
	for name, payload := range benchPayloads {
		b.Run(name, func(b *testing.B) {
			resp := &wrapperspb.BytesValue{Value: payload}
			respBytes, err := proto.Marshal(resp)
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(5 + len(respBytes)))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				writeResponse(io.Discard, respBytes)
			}
		})
	}
}

func BenchmarkReadResponse(b *testing.B) {
	for name, payload := range benchPayloads {
		b.Run(name, func(b *testing.B) {
			resp := &wrapperspb.BytesValue{Value: payload}
			respBytes, err := proto.Marshal(resp)
			if err != nil {
				b.Fatal(err)
			}
			var buf bytes.Buffer
			writeResponse(&buf, respBytes)
			data := buf.Bytes()
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				_, pb, _ := readResponse(bytes.NewReader(data))
				pb.release()
			}
		})
	}
}
