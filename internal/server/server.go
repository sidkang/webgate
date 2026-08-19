package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/sidkang/webgate/internal/search"
)

type Config struct {
	Token   string
	Sources map[string]search.Searcher
}

type Server struct {
	token   string
	sources map[string]search.Searcher
	mux     *http.ServeMux
}

func New(cfg Config) http.Handler {
	sources := cfg.Sources
	if sources == nil {
		sources = map[string]search.Searcher{}
	}
	s := &Server{token: cfg.Token, sources: sources, mux: http.NewServeMux()}
	s.mux.HandleFunc("POST /v1/search", s.handleSearch)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

type searchRequest struct {
	Query    string `json:"query"`
	Limit    *int   `json:"limit"`
	Provider any    `json:"provider"`
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

	plan, perr := search.ParseProvider(req.Provider)
	if perr != nil {
		writeSearchError(w, perr)
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

	result, winner, err := search.SearchChain(r.Context(), s.sources, plan.Chain, query, limit, plan.SkipMissing)
	if err != nil {
		writeSearchError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"answer":   result.Answer,
		"sources":  result.Sources,
		"provider": winner,
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

func writeSearchError(w http.ResponseWriter, err *search.Error) {
	code := err.Code
	writeError(w, httpStatusFor(code), string(code), publicMessage(code))
}

func httpStatusFor(code search.Code) int {
	switch code {
	case search.CodeInvalidInput,
		search.CodeMissingConfig,
		search.CodeUnsupportedModel,
		search.CodeUnsupportedTool,
		search.CodeUnsupportedToolChoice:
		return http.StatusBadRequest
	case search.CodeRateLimited:
		return http.StatusTooManyRequests
	case search.CodeTimeout:
		return http.StatusGatewayTimeout
	case search.CodeAborted:
		return http.StatusInternalServerError
	case search.CodeAuthFailed:
		return http.StatusBadGateway
	case search.CodeInvalidResponse:
		return http.StatusBadGateway
	default:
		return http.StatusBadGateway
	}
}

func publicMessage(code search.Code) string {
	switch code {
	case search.CodeInvalidInput:
		return "invalid input"
	case search.CodeMissingConfig:
		return "search source is not configured"
	case search.CodeAuthFailed:
		return "search authentication failed"
	case search.CodeBackendError:
		return "search backend request failed"
	case search.CodeInvalidResponse:
		return "search returned no answer or sources"
	case search.CodeRateLimited:
		return "search rate limited"
	case search.CodeTimeout:
		return "search timed out"
	case search.CodeAborted:
		return "search was aborted"
	case search.CodeUnsupportedModel:
		return "unsupported model"
	case search.CodeUnsupportedTool:
		return "unsupported tool"
	case search.CodeUnsupportedToolChoice:
		return "unsupported tool choice"
	default:
		return "search failed"
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: message, Code: code})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
