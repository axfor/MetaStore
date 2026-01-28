package http

import "net/http"

const (
	versionPath = "/version"
)

func HandleVersion(mux *http.ServeMux) {
	mux.HandleFunc(versionPath, serveVersion)
}

func serveVersion(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, "GET") {
		return
	}

	w.Header().Set("Content-Type", "application/json")
	// Return versions compatible with etcd clients
	// etcdserver: 3.6.0
	// etcdcluster: 3.5.0 (safe default)
	w.Write([]byte(`{"etcdserver":"3.6.0","etcdcluster":"3.5.0"}`))
}
