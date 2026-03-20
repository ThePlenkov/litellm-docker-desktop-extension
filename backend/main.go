package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

var defaultConfig = `# LiteLLM Proxy Configuration
# https://docs.litellm.ai/docs/proxy/configs
#
# Uncomment and edit the examples below to add your models.
# Use os.environ/VAR_NAME to reference environment variables for API keys.
#
# model_list:
#   - model_name: gpt-4
#     litellm_params:
#       model: openai/gpt-4-turbo
#       api_key: os.environ/OPENAI_API_KEY
#
#   - model_name: claude-sonnet
#     litellm_params:
#       model: anthropic/claude-sonnet-4-20250514
#       api_key: os.environ/ANTHROPIC_API_KEY
#
#   - model_name: llama3
#     litellm_params:
#       model: ollama/llama3
#       api_base: http://host.docker.internal:11434

litellm_settings:
  drop_params: true
  num_retries: 3
  request_timeout: 600

general_settings:
  master_key: os.environ/LITELLM_MASTER_KEY
`

func cors(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

func jsonResp(w http.ResponseWriter, status int, v any) {
	cors(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func main() {
	configPath := flag.String("config", envOr("CONFIG_PATH", "/data/config.yaml"), "path to config.yaml")
	addr := flag.String("addr", ":"+envOr("PORT", "8080"), "listen address")
	healthCheck := flag.Bool("health-check", false, "run health check and exit")
	flag.Parse()

	if *healthCheck {
		resp, err := http.Get("http://localhost" + *addr + "/health")
		if err != nil || resp.StatusCode != 200 {
			os.Exit(1)
		}
		os.Exit(0)
	}

	ensureDefault(*configPath)

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		jsonResp(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	http.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodOptions:
			cors(w)
			w.WriteHeader(http.StatusNoContent)

		case http.MethodGet:
			data, err := os.ReadFile(*configPath)
			if err != nil {
				jsonResp(w, http.StatusNotFound, map[string]string{"error": "config file not found"})
				return
			}
			cors(w)
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Write(data)

		case http.MethodPost:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				jsonResp(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			content := body
			if ct := r.Header.Get("Content-Type"); ct == "application/json" {
				var obj map[string]string
				if json.Unmarshal(body, &obj) == nil {
					if c, ok := obj["config"]; ok {
						content = []byte(c)
					}
				}
			}
			if err := os.MkdirAll(filepath.Dir(*configPath), 0o755); err != nil {
				jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			if err := os.WriteFile(*configPath, content, 0o644); err != nil {
				jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			jsonResp(w, http.StatusOK, map[string]string{"status": "saved"})

		default:
			jsonResp(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	})

	log.Printf("[config-server] listening on %s, config at %s", *addr, *configPath)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

func ensureDefault(path string) {
	if _, err := os.Stat(path); err == nil {
		log.Printf("[config-server] using existing config at %s", path)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(defaultConfig), 0o644); err != nil {
		log.Fatalf("write default config: %v", err)
	}
	log.Printf("[config-server] created default config at %s", path)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
