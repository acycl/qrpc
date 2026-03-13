package qrpc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// statusCode extracts a *Status from err using errors.As and returns it, or
// fails the test if the error is not a *Status.
func statusCode(t *testing.T, err error) *Status {
	t.Helper()
	var st *Status
	if !errors.As(err, &st) {
		t.Fatalf("expected *Status, got %T: %v", err, err)
	}
	return st
}

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
	payload, err := proto.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}

	identity := func(_ context.Context, in *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error) {
		return in, nil
	}
	handler := UnaryHandler(identity)

	var buf bytes.Buffer
	if err := handler(context.Background(), &buf, payload); err != nil {
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
	writeErrorResponse(&buf, Errorf(Internal, "something broke"))

	_, _, err := readResponse(&buf)
	if err == nil {
		t.Fatal("expected error")
	}

	st := statusCode(t, err)
	if st.Code() != Internal {
		t.Fatalf("code = %v, want %v", st.Code(), Internal)
	}
	if st.Message() != "something broke" {
		t.Fatalf("message = %q, want %q", st.Message(), "something broke")
	}
}

func TestRoundTripErrorResponsePlainError(t *testing.T) {
	var buf bytes.Buffer
	writeErrorResponse(&buf, errors.New("plain error"))

	_, _, err := readResponse(&buf)
	if err == nil {
		t.Fatal("expected error")
	}

	st := statusCode(t, err)
	if st.Code() != Unknown {
		t.Fatalf("code = %v, want %v", st.Code(), Unknown)
	}
	if st.Message() != "plain error" {
		t.Fatalf("message = %q, want %q", st.Message(), "plain error")
	}
}

func TestRoundTripErrorResponseWithDetails(t *testing.T) {
	detail := &wrapperspb.StringValue{Value: "field_name"}
	st, err := Errorf(InvalidArgument, "bad field").WithDetails(detail)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	writeErrorResponse(&buf, st)

	_, _, readErr := readResponse(&buf)
	if readErr == nil {
		t.Fatal("expected error")
	}

	got := statusCode(t, readErr)
	if got.Code() != InvalidArgument {
		t.Fatalf("code = %v, want %v", got.Code(), InvalidArgument)
	}
	if len(got.Details()) != 1 {
		t.Fatalf("got %d details, want 1", len(got.Details()))
	}
	sv, ok := got.Details()[0].(*wrapperspb.StringValue)
	if !ok {
		t.Fatalf("detail type = %T, want *wrapperspb.StringValue", got.Details()[0])
	}
	if sv.Value != "field_name" {
		t.Fatalf("detail value = %q, want %q", sv.Value, "field_name")
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

func BenchmarkUnaryHandler(b *testing.B) {
	identity := func(_ context.Context, in *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error) {
		return in, nil
	}
	handler := UnaryHandler(identity)
	ctx := context.Background()

	for name, payload := range benchPayloads {
		b.Run(name, func(b *testing.B) {
			req := &wrapperspb.BytesValue{Value: payload}
			reqBytes, err := proto.Marshal(req)
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(5 + proto.Size(req)))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				handler(ctx, io.Discard, reqBytes)
			}
		})
	}
}

func BenchmarkReadResponse(b *testing.B) {
	identity := func(_ context.Context, in *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error) {
		return in, nil
	}
	handler := UnaryHandler(identity)
	ctx := context.Background()

	for name, payload := range benchPayloads {
		b.Run(name, func(b *testing.B) {
			req := &wrapperspb.BytesValue{Value: payload}
			reqBytes, err := proto.Marshal(req)
			if err != nil {
				b.Fatal(err)
			}
			var buf bytes.Buffer
			handler(ctx, &buf, reqBytes)
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
