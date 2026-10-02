package healthserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

func Handler() http.Handler {
	mux := http.NewServeMux()
	health := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"service": "golangtools",
			"status":  "ok",
		})
	}
	mux.HandleFunc("GET /", health)
	mux.HandleFunc("GET /health", health)
	return mux
}

func Run(listen string) error {
	if strings.TrimSpace(listen) == "" {
		listen = "127.0.0.1:8080"
	}
	server := &http.Server{
		Addr:              listen,
		Handler:           Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
