package etcdgateway

import (
	"context"
	"strings"

	gw "go.etcd.io/etcd/api/v3/etcdserverpb/gw"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
)

// NewHandler creates an HTTP handler that serves the etcd v3 HTTP/JSON gateway routes
// (e.g. /v3/kv/range) and proxies them to the provided gRPC endpoint.
//
// The endpoint should be a gRPC dial target (typically host:port). If a port-only
// address like ":2379" is provided, it will be normalized to "127.0.0.1:2379".
func NewHandler(ctx context.Context, endpoint string, dialOpts []grpc.DialOption, muxOpts ...runtime.ServeMuxOption) (*runtime.ServeMux, error) {
	endpoint = normalizeEndpoint(endpoint)

	// 参考 etcd
	mux := runtime.NewServeMux(
		runtime.WithMarshalerOption(runtime.MIMEWildcard,
			&runtime.HTTPBodyMarshaler{
				Marshaler: &runtime.JSONPb{
					MarshalOptions: protojson.MarshalOptions{
						UseProtoNames:   true,
						EmitUnpopulated: false,
					},
					UnmarshalOptions: protojson.UnmarshalOptions{
						DiscardUnknown: true,
					},
				},
			},
		),
	)

	// Register handlers for services implemented by MetaStore.
	if err := gw.RegisterKVHandlerFromEndpoint(ctx, mux, endpoint, dialOpts); err != nil {
		return nil, err
	}
	if err := gw.RegisterWatchHandlerFromEndpoint(ctx, mux, endpoint, dialOpts); err != nil {
		return nil, err
	}
	if err := gw.RegisterLeaseHandlerFromEndpoint(ctx, mux, endpoint, dialOpts); err != nil {
		return nil, err
	}
	if err := gw.RegisterClusterHandlerFromEndpoint(ctx, mux, endpoint, dialOpts); err != nil {
		return nil, err
	}
	if err := gw.RegisterMaintenanceHandlerFromEndpoint(ctx, mux, endpoint, dialOpts); err != nil {
		return nil, err
	}
	if err := gw.RegisterAuthHandlerFromEndpoint(ctx, mux, endpoint, dialOpts); err != nil {
		return nil, err
	}

	return mux, nil
}

// TODO 使用 listen-client-urls 配置来规范化 endpoint
func normalizeEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if strings.HasPrefix(endpoint, ":") {
		return "127.0.0.1" + endpoint
	}
	if strings.HasPrefix(endpoint, "0.0.0.0:") {
		return "127.0.0.1" + strings.TrimPrefix(endpoint, "0.0.0.0")
	}
	return endpoint
}
