package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

var update = flag.Bool("update", false, "update golden files")

// unaryMethod returns a MethodDescriptorProto for a unary RPC.
func unaryMethod(name, input, output string) *descriptorpb.MethodDescriptorProto {
	return &descriptorpb.MethodDescriptorProto{
		Name:       proto.String(name),
		InputType:  proto.String(input),
		OutputType: proto.String(output),
	}
}

// streamingMethod returns a MethodDescriptorProto with the given streaming
// flags set.
func streamingMethod(name, input, output string, clientStream, serverStream bool) *descriptorpb.MethodDescriptorProto {
	return &descriptorpb.MethodDescriptorProto{
		Name:            proto.String(name),
		InputType:       proto.String(input),
		OutputType:      proto.String(output),
		ClientStreaming: proto.Bool(clientStream),
		ServerStreaming: proto.Bool(serverStream),
	}
}

// testFile builds a minimal FileDescriptorProto for testing code generation.
func testFile(name, pkg, goPkg string, msgs []string, svcs []*descriptorpb.ServiceDescriptorProto) *descriptorpb.FileDescriptorProto {
	var msgTypes []*descriptorpb.DescriptorProto
	for _, m := range msgs {
		msgTypes = append(msgTypes, &descriptorpb.DescriptorProto{Name: proto.String(m)})
	}
	return &descriptorpb.FileDescriptorProto{
		Name:        proto.String(name),
		Package:     proto.String(pkg),
		Options:     &descriptorpb.FileOptions{GoPackage: proto.String(goPkg)},
		MessageType: msgTypes,
		Service:     svcs,
		Syntax:      proto.String("proto3"),
	}
}

// runPlugin creates a protogen.Plugin from the given file descriptors, runs
// generate, and returns the CodeGeneratorResponse.
func runPlugin(t *testing.T, files ...*descriptorpb.FileDescriptorProto) (*pluginpb.CodeGeneratorResponse, error) {
	t.Helper()
	var names []string
	for _, f := range files {
		names = append(names, f.GetName())
	}
	req := &pluginpb.CodeGeneratorRequest{
		FileToGenerate: names,
		ProtoFile:      files,
	}
	plugin, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatalf("protogen.Options.New: %v", err)
	}
	if err := generate(plugin); err != nil {
		return nil, err
	}
	return plugin.Response(), nil
}

func TestGenerateUnaryService(t *testing.T) {
	f := testFile("greeter.proto", "greeter", "example.com/greeter",
		[]string{"HelloRequest", "HelloReply"},
		[]*descriptorpb.ServiceDescriptorProto{
			{
				Name: proto.String("Greeter"),
				Method: []*descriptorpb.MethodDescriptorProto{
					unaryMethod("SayHello", ".greeter.HelloRequest", ".greeter.HelloReply"),
				},
			},
		},
	)

	resp, err := runPlugin(t, f)
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetError() != "" {
		t.Fatalf("plugin error: %s", resp.GetError())
	}
	if len(resp.File) != 1 {
		t.Fatalf("got %d files, want 1", len(resp.File))
	}

	got := resp.File[0].GetContent()
	golden := filepath.Join("testdata", "greeter.golden")

	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden file (run with -update to create): %v", err)
	}

	if got != string(want) {
		t.Errorf("generated output differs from golden file %s\n\ngot:\n%s", golden, got)
	}
}

func TestStreamingMethodError(t *testing.T) {
	tests := []struct {
		name   string
		method *descriptorpb.MethodDescriptorProto
	}{
		{
			name:   "server streaming",
			method: streamingMethod("Watch", ".test.Req", ".test.Resp", false, true),
		},
		{
			name:   "client streaming",
			method: streamingMethod("Upload", ".test.Req", ".test.Resp", true, false),
		},
		{
			name:   "bidirectional streaming",
			method: streamingMethod("Chat", ".test.Req", ".test.Resp", true, true),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := testFile("test.proto", "test", "example.com/test",
				[]string{"Req", "Resp"},
				[]*descriptorpb.ServiceDescriptorProto{
					{
						Name:   proto.String("TestService"),
						Method: []*descriptorpb.MethodDescriptorProto{tt.method},
					},
				},
			)

			_, err := runPlugin(t, f)
			if err == nil {
				t.Fatal("expected error for streaming method")
			}
			if !strings.Contains(err.Error(), "streaming") {
				t.Errorf("error %q does not mention streaming", err)
			}
			if !strings.Contains(err.Error(), tt.method.GetName()) {
				t.Errorf("error %q does not mention method name %q", err, tt.method.GetName())
			}
		})
	}
}

func TestMultipleServices(t *testing.T) {
	f := testFile("multi.proto", "multi", "example.com/multi",
		[]string{"Req", "Resp"},
		[]*descriptorpb.ServiceDescriptorProto{
			{
				Name: proto.String("Alpha"),
				Method: []*descriptorpb.MethodDescriptorProto{
					unaryMethod("DoAlpha", ".multi.Req", ".multi.Resp"),
				},
			},
			{
				Name: proto.String("Beta"),
				Method: []*descriptorpb.MethodDescriptorProto{
					unaryMethod("DoBeta", ".multi.Req", ".multi.Resp"),
				},
			},
		},
	)

	resp, err := runPlugin(t, f)
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetError() != "" {
		t.Fatalf("plugin error: %s", resp.GetError())
	}
	if len(resp.File) != 1 {
		t.Fatalf("got %d files, want 1", len(resp.File))
	}

	content := resp.File[0].GetContent()
	for _, want := range []string{
		"type AlphaServer interface",
		"type AlphaClient interface",
		"func RegisterAlphaServer(",
		"func NewAlphaClient(",
		`"/multi.Alpha/DoAlpha"`,
		"type BetaServer interface",
		"type BetaClient interface",
		"func RegisterBetaServer(",
		"func NewBetaClient(",
		`"/multi.Beta/DoBeta"`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("output missing %q", want)
		}
	}
}

func TestNoServices(t *testing.T) {
	f := testFile("empty.proto", "empty", "example.com/empty",
		[]string{"Msg"}, nil,
	)

	resp, err := runPlugin(t, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.File) != 0 {
		t.Fatalf("got %d files, want 0 for proto with no services", len(resp.File))
	}
}
