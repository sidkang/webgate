package server

import (
	_ "embed"
	"net/http"
	"strconv"
)

//go:embed debug.html
var debugHTML []byte

func (s *Server) registerDebugUI() {
	s.mux.HandleFunc("GET /debug", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("Content-Length", strconv.Itoa(len(debugHTML)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(debugHTML)
	})
	s.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/debug", http.StatusFound)
	})
}
