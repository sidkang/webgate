package main

import (
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/sidkang/webgate/internal/search"
	"github.com/sidkang/webgate/internal/server"
)

func main() {
	addr := os.Getenv("WEBGATE_ADDR")
	if addr == "" {
		addr = ":8787"
	}
	token := os.Getenv("WEBGATE_TOKEN")
	if token == "" {
		log.Fatal("WEBGATE_TOKEN is required")
	}

	sources := map[string]search.Searcher{}
	if base := os.Getenv("SEARXNG_BASE_URL"); base != "" {
		sources[search.SourceSearXNG] = search.SearXNG{BaseURL: base}
	}
	// OpenAI / xAI HTTP clients are wired in a later ticket.

	cfg := server.Config{
		Token:         token,
		Sources:       sources,
		CloakDisabled: envTruthy("WEBGATE_CLOAK_DISABLED"),
		CDPEndpoint:   strings.TrimSpace(os.Getenv("CDP_ENDPOINT")),
		CDPAPIKey:     strings.TrimSpace(os.Getenv("CDP_API_KEY")),
	}

	log.Printf("webgate listening on %s", addr)
	if err := http.ListenAndServe(addr, server.New(cfg)); err != nil {
		log.Fatal(err)
	}
}

func envTruthy(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return v == "1" || v == "true" || v == "yes"
}
