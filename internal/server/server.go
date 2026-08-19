package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/sidkang/webgate/internal/fetch"
	"github.com/sidkang/webgate/internal/search"
)

type Config struct {
	Token          string
	Sources        map[string]search.Searcher
	XSearch        *search.XSearch
	CloakDisabled  bool
	CDPEndpoint    string
	CDPAPIKey      string
	Capturer       fetch.Capturer
	Kernels        fetch.KernelRunners
	MaxInlineChars int
	FetchTimeout   time.Duration
	Lookup         fetch.Lookup
}

type Server struct {
	token   string
	sources map[string]search.Searcher
	xsearch *search.XSearch
	fetch   *fetch.Service
	mux     *http.ServeMux
}

func New(cfg Config) http.Handler {
	sources := cfg.Sources
	if sources == nil {
		sources = map[string]search.Searcher{}
	}
	s := &Server{
		token:   cfg.Token,
		sources: sources,
		xsearch: cfg.XSearch,
		fetch: fetch.NewService(fetch.ServiceConfig{
			CloakDisabled:  cfg.CloakDisabled,
			CDPEndpoint:    cfg.CDPEndpoint,
			CDPAPIKey:      cfg.CDPAPIKey,
			Capturer:       cfg.Capturer,
			Kernels:        cfg.Kernels,
			MaxInlineChars: cfg.MaxInlineChars,
			FetchTimeout:   cfg.FetchTimeout,
			Lookup:         cfg.Lookup,
		}),
		mux: http.NewServeMux(),
	}
	s.mux.HandleFunc("POST /v1/search", s.handleSearch)
	s.mux.HandleFunc("POST /v1/fetch", s.handleFetch)
	s.mux.HandleFunc("POST /v1/x_search", s.handleXSearch)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

type searchRequest struct {
	Query             string               `json:"query"`
	Limit             *int                 `json:"limit"`
	Provider          any                  `json:"provider"`
	SearchContextSize any                  `json:"search_context_size"`
	AllowedDomains    []string             `json:"allowed_domains"`
	UserLocation      *search.UserLocation `json:"user_location"`
}

type xSearchRequest struct {
	Query    string `json:"query"`
	FromDate string `json:"from_date"`
	ToDate   string `json:"to_date"`
}

type fetchRequest struct {
	URL     string          `json:"url"`
	Mode    any             `json:"mode"`
	Kernel  any             `json:"kernel"`
	Profile json.RawMessage `json:"profile"` // key present (incl. null) → invalid_input
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
	ctxSize, cerr := search.ParseSearchContextSize(req.SearchContextSize)
	if cerr != nil {
		writeSearchError(w, cerr)
		return
	}

	searchReq := search.Request{
		Query:             query,
		Limit:             limit,
		SearchContextSize: ctxSize,
		AllowedDomains:    req.AllowedDomains,
		UserLocation:      req.UserLocation,
	}
	result, winner, err := search.SearchChain(r.Context(), s.sources, plan.Chain, searchReq, plan.SkipMissing)
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

func (s *Server) handleXSearch(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "auth_failed", "missing or invalid bearer token")
		return
	}
	var req xSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_input", "request body must be JSON")
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		writeError(w, http.StatusBadRequest, "invalid_input", "query is required")
		return
	}
	if s.xsearch == nil {
		writeSearchError(w, search.NewError(search.CodeMissingConfig, "x_search is not configured"))
		return
	}
	result, err := s.xsearch.Search(r.Context(), search.XSearchRequest{
		Query:    query,
		FromDate: req.FromDate,
		ToDate:   req.ToDate,
	})
	if err != nil {
		classified := search.Classify(err)
		writeSearchError(w, search.NewError(classified.Code, classified.Message))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"answer":   result.Answer,
		"sources":  result.Sources,
		"provider": "xai",
	})
}

func (s *Server) handleFetch(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "auth_failed", "missing or invalid bearer token")
		return
	}

	var req fetchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_input", "request body must be JSON")
		return
	}
	if len(req.Profile) > 0 {
		writeError(w, http.StatusBadRequest, "invalid_input", "profile must not be sent by clients")
		return
	}
	url := strings.TrimSpace(req.URL)
	if url == "" {
		writeError(w, http.StatusBadRequest, "invalid_input", "url is required")
		return
	}
	mode, merr := fetch.ParseMode(req.Mode)
	if merr != nil {
		writeFetchError(w, merr)
		return
	}
	kernel, kerr := fetch.ParseKernel(req.Kernel)
	if kerr != nil {
		writeFetchError(w, kerr)
		return
	}

	result, err := s.fetch.Fetch(r.Context(), url, mode, kernel)
	if err != nil {
		var fe *fetch.Error
		if errors.As(err, &fe) {
			writeFetchError(w, fe)
			return
		}
		writeError(w, http.StatusBadGateway, "backend_error", "fetch failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"url":           result.URL,
		"requestedUrl":  result.RequestedURL,
		"title":         result.Title,
		"content":       result.Content,
		"mode":          result.Mode,
		"kernel":        result.Kernel,
		"truncated":    result.Truncated,
		"totalChars":    result.TotalChars,
		"returnedChars": result.ReturnedChars,
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
	writeError(w, httpStatusForSearch(code), string(code), publicSearchMessage(code))
}

func writeFetchError(w http.ResponseWriter, err *fetch.Error) {
	code := err.Code
	msg := publicFetchMessage(code)
	switch code {
	case fetch.CodeInvalidInput, fetch.CodeMissingConfig, fetch.CodeCloakDisabled:
		if err.Message != "" {
			msg = err.Message
		}
	}
	writeError(w, httpStatusForFetch(code), string(code), msg)
}

func httpStatusForSearch(code search.Code) int {
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

func httpStatusForFetch(code fetch.Code) int {
	switch code {
	case fetch.CodeInvalidInput, fetch.CodeMissingConfig:
		return http.StatusBadRequest
	case fetch.CodeCloakDisabled:
		return http.StatusServiceUnavailable
	case fetch.CodeTimeout:
		return http.StatusGatewayTimeout
	case fetch.CodeAborted:
		return http.StatusInternalServerError
	default:
		return http.StatusBadGateway
	}
}

func publicSearchMessage(code search.Code) string {
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

func publicFetchMessage(code fetch.Code) string {
	switch code {
	case fetch.CodeInvalidInput:
		return "invalid input"
	case fetch.CodeMissingConfig:
		return "fetch is not configured"
	case fetch.CodeCloakDisabled:
		return "Cloak browser access is disabled"
	case fetch.CodeBackendError:
		return "fetch backend request failed"
	case fetch.CodeTimeout:
		return "fetch timed out"
	case fetch.CodeAborted:
		return "fetch was aborted"
	default:
		return "fetch failed"
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
