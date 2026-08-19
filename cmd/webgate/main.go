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

	cfg := server.Config{Token: token}
	if base := os.Getenv("SEARXNG_BASE_URL"); base != "" {
		cfg.Searcher = search.SearXNG{BaseURL: base}
	}

	log.Printf("webgate listening on %s", addr)
	if err := http.ListenAndServe(addr, server.New(cfg)); err != nil {
		log.Fatal(err)
	}
}
