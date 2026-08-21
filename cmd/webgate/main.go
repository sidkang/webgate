package main

import (
	"log"
	"net/http"
	"os"
	"strings"
	"time"

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

	cfg := server.Config{
		Token:         token,
		Sources:       sources,
		CloakDisabled: envTruthy("WEBGATE_CLOAK_DISABLED"),
		CDPEndpoint:   strings.TrimSpace(os.Getenv("CDP_ENDPOINT")),
		CDPAPIKey:     strings.TrimSpace(os.Getenv("CDP_API_KEY")),
	}

	if openai := openAIFromEnv(); openai != nil {
		sources[search.SourceOpenAI] = *openai
		cfg.Merger = search.OpenAIMerger{Client: openai.Client, Model: openai.Model}
	}
	if xai := xaiFromEnv(); xai != nil {
		sources[search.SourceXAI] = *xai
		cfg.XSearch = &search.XSearch{Client: xai.Client, Model: xai.Model}
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           server.New(cfg),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      130 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}

	log.Printf("webgate listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func envTruthy(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return v == "1" || v == "true" || v == "yes"
}

func openAIFromEnv() *search.OpenAI {
	base := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL"))
	key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	model := strings.TrimSpace(os.Getenv("OPENAI_MODEL"))
	if base == "" || model == "" || key == "" {
		return nil
	}
	return &search.OpenAI{
		Client: &search.ResponsesClient{BaseURL: base, APIKey: key},
		Model:  model,
	}
}

func xaiFromEnv() *search.XAI {
	base := strings.TrimSpace(os.Getenv("XAI_BASE_URL"))
	key := strings.TrimSpace(os.Getenv("XAI_API_KEY"))
	model := strings.TrimSpace(os.Getenv("XAI_MODEL"))
	if base == "" || model == "" || key == "" {
		return nil
	}
	if err := search.RejectOfficialXAIBaseURL(base); err != nil {
		log.Printf("XAI_BASE_URL rejected (official api.x.ai): not registering xai / x_search")
		return nil
	}
	return &search.XAI{
		Client: &search.ResponsesClient{BaseURL: base, APIKey: key},
		Model:  model,
	}
}
