package qrpc

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/acycl/qrpc/internal/statuspb"
)

// Code is an RPC status code. The canonical values mirror the gRPC status code
// taxonomy so that the semantics are immediately familiar.
type Code uint32

const (
	// OK indicates the operation completed successfully.
	OK Code = 0

	// Canceled indicates the operation was canceled, typically by the caller.
	Canceled Code = 1

	// Unknown is the default code for errors that do not carry a status code.
	Unknown Code = 2

	// InvalidArgument indicates the client supplied an invalid argument.
	InvalidArgument Code = 3

	// DeadlineExceeded indicates the operation expired before completion.
	DeadlineExceeded Code = 4

	// NotFound indicates the requested entity was not found.
	NotFound Code = 5

	// AlreadyExists indicates the entity the caller tried to create already
	// exists.
	AlreadyExists Code = 6

	// PermissionDenied indicates the caller lacks permission for the
	// operation.
	PermissionDenied Code = 7

	// ResourceExhausted indicates some resource has been exhausted, such as a
	// per-user quota or file system space.
	ResourceExhausted Code = 8

	// Unimplemented indicates the operation is not implemented or not
	// supported.
	Unimplemented Code = 12

	// Internal indicates an internal server error.
	Internal Code = 13

	// Unavailable indicates the service is currently unavailable, typically a
	// transient condition.
	Unavailable Code = 14

	// Unauthenticated indicates the request lacks valid authentication
	// credentials.
	Unauthenticated Code = 16
)

// String returns the canonical name of the status code.
func (c Code) String() string {
	switch c {
	case OK:
		return "OK"
	case Canceled:
		return "Canceled"
	case Unknown:
		return "Unknown"
	case InvalidArgument:
		return "InvalidArgument"
	case DeadlineExceeded:
		return "DeadlineExceeded"
	case NotFound:
		return "NotFound"
	case AlreadyExists:
		return "AlreadyExists"
	case PermissionDenied:
		return "PermissionDenied"
	case ResourceExhausted:
		return "ResourceExhausted"
	case Unimplemented:
		return "Unimplemented"
	case Internal:
		return "Internal"
	case Unavailable:
		return "Unavailable"
	case Unauthenticated:
		return "Unauthenticated"
	default:
		return fmt.Sprintf("Code(%d)", c)
	}
}

// Status represents an RPC error with a status code, human-readable message,
// and optional structured details. It implements the error interface and can be
// extracted from errors returned by Invoke using errors.As.
type Status struct {
	code    Code
	message string
	details []proto.Message
}

// Error implements the error interface.
func (s *Status) Error() string {
	return fmt.Sprintf("qrpc: %s: %s", s.code, s.message)
}

// Code returns the status code.
func (s *Status) Code() Code { return s.code }

// Message returns the human-readable error message.
func (s *Status) Message() string { return s.message }

// Details returns the attached detail messages. Each detail is a proto.Message
// that was either unpacked from a google.protobuf.Any on the wire, or attached
// locally via WithDetails.
func (s *Status) Details() []proto.Message { return s.details }

// WithDetails returns a copy of s with the provided detail messages appended.
// Each detail must be a proto.Message whose type is registered with the global
// protobuf registry, since it will be packed into a google.protobuf.Any for
// wire transmission.
func (s *Status) WithDetails(details ...proto.Message) (*Status, error) {
	for _, d := range details {
		if _, err := anypb.New(d); err != nil {
			return nil, fmt.Errorf("qrpc: pack detail: %w", err)
		}
	}
	cp := *s
	cp.details = make([]proto.Message, len(s.details)+len(details))
	copy(cp.details, s.details)
	copy(cp.details[len(s.details):], details)
	return &cp, nil
}

// Errorf creates a *Status error with the given code and formatted message.
func Errorf(code Code, format string, args ...any) *Status {
	return &Status{
		code:    code,
		message: fmt.Sprintf(format, args...),
	}
}

// FromError converts an arbitrary error to a *Status. If err already wraps a
// *Status, the original status is returned. Context errors are mapped to their
// canonical codes. All other errors produce a Status with code Unknown.
func FromError(err error) *Status {
	if err == nil {
		return nil
	}
	var st *Status
	if errors.As(err, &st) {
		return st
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Status{code: DeadlineExceeded, message: err.Error()}
	}
	if errors.Is(err, context.Canceled) {
		return &Status{code: Canceled, message: err.Error()}
	}
	return &Status{code: Unknown, message: err.Error()}
}

// CodeFromError returns the status code for err. If err is nil, OK is
// returned. If err does not wrap a *Status, Unknown is returned.
func CodeFromError(err error) Code {
	if err == nil {
		return OK
	}
	var st *Status
	if errors.As(err, &st) {
		return st.code
	}
	return Unknown
}

// statusProto converts a *Status to its protocol buffer representation for
// wire serialization.
func statusProto(s *Status) *statuspb.Status {
	sp := &statuspb.Status{
		Code:    int32(s.code),
		Message: s.message,
	}
	for _, d := range s.details {
		a, err := anypb.New(d)
		if err != nil {
			continue
		}
		sp.Details = append(sp.Details, a)
	}
	return sp
}

// unmarshalStatus deserializes a *Status from wire bytes. Details that cannot
// be unpacked (e.g. because the type is not registered) are preserved as raw
// *anypb.Any values.
func unmarshalStatus(b []byte) (*Status, error) {
	sp := new(statuspb.Status)
	if err := proto.Unmarshal(b, sp); err != nil {
		return nil, err
	}
	s := &Status{
		code:    Code(sp.Code),
		message: sp.Message,
	}
	for _, a := range sp.Details {
		msg, err := a.UnmarshalNew()
		if err != nil {
			s.details = append(s.details, a)
			continue
		}
		s.details = append(s.details, msg)
	}
	return s, nil
}
