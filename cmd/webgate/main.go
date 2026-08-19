package main

import (
	"log"
	"net/http"
	"os"

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

	cfg := server.Config{Token: token, Sources: sources}

	log.Printf("webgate listening on %s", addr)
	if err := http.ListenAndServe(addr, server.New(cfg)); err != nil {
		log.Fatal(err)
	}
}
