package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/sidkang/webgate/internal/search"
)

type Config struct {
	Token    string
	Searcher search.Searcher
}

type Server struct {
	token    string
	searcher search.Searcher
	mux      *http.ServeMux
}

func New(cfg Config) http.Handler {
	s := &Server{token: cfg.Token, searcher: cfg.Searcher, mux: http.NewServeMux()}
	s.mux.HandleFunc("POST /v1/search", s.handleSearch)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

type searchRequest struct {
	Query    string `json:"query"`
	Limit    *int   `json:"limit"`
	Provider string `json:"provider"`
}

type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "auth_failed", "missing or invalid bearer token")
		return
	}

	var req searchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_input", "request body must be JSON")
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		writeError(w, http.StatusBadRequest, "invalid_input", "query is required")
		return
	}
	if req.Provider != "searxng" {
		writeError(w, http.StatusBadRequest, "invalid_input", "provider must be searxng")
		return
	}
	limit := 10
	if req.Limit != nil {
		limit = *req.Limit
		if limit < 1 || limit > 20 {
			writeError(w, http.StatusBadRequest, "invalid_input", "limit must be between 1 and 20")
			return
		}
	}
	if s.searcher == nil {
		writeError(w, http.StatusBadRequest, "missing_config", "SearXNG is not configured")
		return
	}

	result, err := s.searcher.Search(r.Context(), query, limit)
	if err != nil {
		writeError(w, http.StatusBadGateway, "backend_error", err.Error())
		return
	}
	if search.IsEmpty(result) {
		writeError(w, http.StatusBadGateway, "invalid_response", "search returned no answer or sources")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"answer":   result.Answer,
		"sources":  result.Sources,
		"provider": "searxng",
	})
}

func (s *Server) authorized(r *http.Request) bool {
	if s.token == "" {
		return false
	}
	got := r.Header.Get("Authorization")
	want := "Bearer " + s.token
	return got == want
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: message, Code: code})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
