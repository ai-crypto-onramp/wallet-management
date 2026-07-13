package main

import (
	"encoding/json"
	"net/http"
	"time"
)

func healthzHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// newMux builds the HTTP routing table for the service.
func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthzHandler)
	return mux
}

// run starts the HTTP server on addr and blocks until the server exits.
func run(addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           newMux(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return srv.ListenAndServe()
}

func main() {
	_ = run(":8080")
}
