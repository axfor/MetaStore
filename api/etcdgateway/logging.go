package etcdgateway

import (
	"net/http"

	"metaStore/pkg/log"

	"go.uber.org/zap"
)

// WithLogging wraps an HTTP handler and emits a structured log line per request.
func WithLogging(next http.Handler) http.Handler {
	if next == nil {
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Info("grpc-gateway request received",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.String("component", "grpc-gateway"),
		)
		next.ServeHTTP(w, r)
	})
}
