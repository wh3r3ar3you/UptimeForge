//go:build !lite

package checks

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/bufbuild/protocompile"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func init() {
	registerType(TypeInfo{
		Type: "grpc-keyword", Category: "web", Keyword: true,
		Fields: []Field{fHost, fPort("443"),
			{Name: "proto", Kind: "textarea", Required: true},
			{Name: "service", Kind: "text", Required: true},
			{Name: "method", Kind: "text", Required: true},
			{Name: "body", Kind: "textarea", Secret: true},
			{Name: "metadata", Kind: "textarea", Secret: true, Advanced: true},
			{Name: "tls", Kind: "bool", Default: "true"},
			fInsecure,
		},
		target: targetAddr,
		validate: func(p params) error {
			if _, err := parseHeaders(p["metadata"]); err != nil {
				return fmt.Errorf("parameter metadata %w", err)
			}
			_, err := grpcMethod(context.Background(), p)
			return err
		},
	}, checkGRPC)
}

// grpcMethod compiles the user's .proto text and finds the method to call.
func grpcMethod(ctx context.Context, p params) (protoreflect.MethodDescriptor, error) {
	const file = "check.proto"
	c := protocompile.Compiler{Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{
		Accessor: protocompile.SourceAccessorFromMap(map[string]string{file: p["proto"]}),
	})}
	files, err := c.Compile(ctx, file)
	if err != nil {
		return nil, fmt.Errorf("proto does not compile: %w", err)
	}
	fd := files[0]
	svcs := fd.Services()
	for i := range svcs.Len() {
		svc := svcs.Get(i)
		if string(svc.FullName()) != p["service"] && string(svc.Name()) != p["service"] {
			continue
		}
		m := svc.Methods().ByName(protoreflect.Name(p["method"]))
		if m == nil {
			return nil, fmt.Errorf("service %s has no method %s", p["service"], p["method"])
		}
		if m.IsStreamingClient() || m.IsStreamingServer() {
			return nil, fmt.Errorf("method %s is streaming, only unary calls are checked", p["method"])
		}
		return m, nil
	}
	return nil, fmt.Errorf("proto has no service %s", p["service"])
}

func checkGRPC(ctx context.Context, s *Spec, env *Env) Result {
	p := paramsOf(s)
	m, err := grpcMethod(ctx, p)
	if err != nil {
		return fail("config", err)
	}
	in, out := dynamicpb.NewMessage(m.Input()), dynamicpb.NewMessage(m.Output())
	if body := p["body"]; strings.TrimSpace(body) != "" {
		if err := protojson.Unmarshal([]byte(body), in); err != nil {
			return fail("config", fmt.Errorf("body does not match %s", m.Input().FullName()))
		}
	}
	md, err := parseHeaders(p["metadata"])
	if err != nil {
		return fail("config", fmt.Errorf("metadata %w", err))
	}
	if len(md) > 0 {
		ctx = metadata.NewOutgoingContext(ctx, metadata.New(md))
	}
	creds := insecure.NewCredentials()
	if p.bool("tls") {
		creds = credentials.NewTLS(&tls.Config{ServerName: p["host"], InsecureSkipVerify: p.bool("insecure")})
	}
	conn, err := grpc.NewClient("passthrough:///"+p.addr(),
		grpc.WithTransportCredentials(creds),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			return env.Dial(ctx, "tcp", addr)
		}))
	if err != nil {
		return fail("config", err)
	}
	defer conn.Close()
	start := time.Now()
	fullMethod := "/" + string(m.Parent().FullName()) + "/" + string(m.Name())
	if err := conn.Invoke(ctx, fullMethod, in, out, grpc.WaitForReady(false)); err != nil {
		return failGRPC(ctx, err)
	}
	text, err := protojson.Marshal(out)
	if err != nil {
		return fail("bad_response", errors.New("answer cannot be printed as JSON"))
	}
	return Result{LatencyMS: ms(time.Since(start)), Text: string(text)}
}

// failGRPC keeps transport trouble under its usual reason codes; the rest is
// the service speaking, which is grpc_status.
func failGRPC(ctx context.Context, err error) Result {
	st, ok := status.FromError(err)
	if !ok {
		return fail(reasonOf(ctx, err), err)
	}
	msg := fmt.Errorf("gRPC %s: %s", st.Code(), clip(st.Message(), 200))
	switch st.Code() {
	case codes.DeadlineExceeded, codes.Canceled:
		return fail("timeout", msg)
	case codes.Unauthenticated, codes.PermissionDenied:
		return fail("auth", msg)
	case codes.Unavailable:
		return fail(reasonOf(ctx, err), msg)
	}
	return fail("grpc_status", msg)
}
