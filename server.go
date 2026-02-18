package main

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// NewRouter sets up all routes and returns the top-level handler.
func (s *Server) NewRouter(corsOrigin string) http.Handler {
	mux := http.NewServeMux()

	// Public endpoints
	mux.HandleFunc("GET /vapid-public-key", s.HandleGetVAPIDPublicKey)
	mux.HandleFunc("POST /subscriptions", s.HandlePostSubscription)
	mux.HandleFunc("DELETE /subscriptions", s.HandleDeleteSubscriptionByEndpoint)

	// Admin endpoints
	mux.HandleFunc("GET /subscriptions", s.requireAuth(s.HandleListSubscriptions))
	mux.HandleFunc("DELETE /subscriptions/{id}", s.requireAuth(s.HandleDeleteSubscriptionByID))
	mux.HandleFunc("POST /notify", s.requireAuth(s.HandleNotify))
	mux.HandleFunc("DELETE /delivery-log", s.requireAuth(s.HandlePurgeDeliveryLog))

	// Apply middleware stack: CORS → logging → content-type validation
	var handler http.Handler = mux
	handler = contentTypeMiddleware(handler)
	handler = loggingMiddleware(handler)
	handler = corsMiddleware(corsOrigin)(handler)

	return handler
}

// corsMiddleware sets CORS headers and handles preflight OPTIONS requests.
func corsMiddleware(origin string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// loggingMiddleware logs method, path, status, and duration for each request.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, sw.status, time.Since(start).Round(time.Millisecond))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

// contentTypeMiddleware validates Content-Type for POST and DELETE with body.
func contentTypeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method == http.MethodPost || r.Method == http.MethodDelete) && r.ContentLength > 0 {
			ct := r.Header.Get("Content-Type")
			if !strings.HasPrefix(ct, "application/json") {
				writeError(w, http.StatusUnsupportedMediaType, fmt.Sprintf("Content-Type must be application/json, got %q", ct))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
