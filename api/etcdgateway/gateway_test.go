package etcdgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	etcdapi "metaStore/api/etcd"
	"metaStore/internal/memory"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func newGatewayTestServer(t *testing.T) (*etcdapi.Server, *httptest.Server) {
	t.Helper()

	grpcListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv, err := etcdapi.NewServer(etcdapi.ServerConfig{
		Store:     memory.NewMemoryEtcd(),
		Listener:  grpcListener,
		ClusterID: 1,
		MemberID:  1,
	})
	require.NoError(t, err)

	go func() {
		_ = srv.Start()
	}()

	handler, err := NewHandler(
		context.Background(),
		grpcListener.Addr().String(),
		[]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())},
	)
	require.NoError(t, err)

	httpSrv := httptest.NewServer(handler)
	t.Cleanup(func() {
		httpSrv.Close()
		srv.Stop()
	})

	return srv, httpSrv
}

func postJSON(t *testing.T, url string, body string) []byte {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(payload))

	return payload
}

func postJSONStream(t *testing.T, url string, body string) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	return resp
}

func TestHTTPLeaseKeepAliveReturnsSingleJSONDocument(t *testing.T) {
	_, httpSrv := newGatewayTestServer(t)

	grantBody := postJSON(t, httpSrv.URL+"/v3/lease/grant", `{"TTL":30}`)

	var grantResp struct {
		ID string `json:"ID"`
	}
	require.NoError(t, json.Unmarshal(grantBody, &grantResp), string(grantBody))
	require.NotEmpty(t, grantResp.ID)

	keepaliveBody := postJSON(t, httpSrv.URL+"/v3/lease/keepalive", `{"ID":"`+grantResp.ID+`"}`)

	var keepaliveResp map[string]any
	require.NoError(t, json.Unmarshal(keepaliveBody, &keepaliveResp), string(keepaliveBody))

	result, ok := keepaliveResp["result"].(map[string]any)
	require.True(t, ok, string(keepaliveBody))

	header, ok := result["header"].(map[string]any)
	require.True(t, ok, string(keepaliveBody))
	require.NotEmpty(t, header["revision"])
}

func TestHTTPWatchCreateResponseIncludesRevision(t *testing.T) {
	_, httpSrv := newGatewayTestServer(t)

	resp := postJSONStream(t, httpSrv.URL+"/v3/watch", `{"create_request":{"key":"Zm9v"}}`)
	defer resp.Body.Close()

	var watchResp map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&watchResp))

	result, ok := watchResp["result"].(map[string]any)
	require.True(t, ok)

	header, ok := result["header"].(map[string]any)
	require.True(t, ok)
	require.NotEmpty(t, header["revision"])
}
