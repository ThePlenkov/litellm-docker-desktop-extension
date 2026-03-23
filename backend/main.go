package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
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
  database_url: os.environ/DATABASE_URL
`

// SecretsConfig stores how each secret should be resolved.
type SecretsConfig struct {
	Secrets map[string]SecretEntry `json:"secrets"`
}

// SecretEntry describes a single secret: either a direct value or a host command.
type SecretEntry struct {
	// "direct" or "command"
	Mode string `json:"mode"`
	// The literal value (mode=direct) or the shell command (mode=command).
	Value string `json:"value"`
}

func cors(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

func jsonResp(w http.ResponseWriter, status int, v any) {
	cors(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// secretsDir returns the directory where secret values are stored.
func secretsDir(dataDir string) string {
	return filepath.Join(dataDir, "secrets")
}

// secretsConfigPath returns the path to the secrets configuration file.
func secretsConfigPath(dataDir string) string {
	return filepath.Join(dataDir, "secrets-config.json")
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

	dataDir := filepath.Dir(*configPath)
	ensureDefault(*configPath)
	os.MkdirAll(secretsDir(dataDir), 0o700)

	litellmURL := envOr("LITELLM_URL", "http://litellm:4000")

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		jsonResp(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	http.HandleFunc("/litellm-health", func(w http.ResponseWriter, r *http.Request) {
		resp, err := http.Get(litellmURL + "/health/liveliness")
		if err != nil {
			jsonResp(w, http.StatusBadGateway, map[string]string{"status": "unreachable"})
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		cors(w)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
	})

	// /litellm-model-health — proxy to LiteLLM /health with auth, returns per-model status.
	http.HandleFunc("/litellm-model-health", func(w http.ResponseWriter, r *http.Request) {
		cors(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// Read the master key from the shared volume (same file the entrypoint uses).
		masterKey := "sk-1234"
		if data, err := os.ReadFile(filepath.Join(secretsDir(dataDir), "master_key")); err == nil {
			if mk := strings.TrimSpace(string(data)); mk != "" {
				masterKey = mk
			}
		}
		req, err := http.NewRequest("GET", litellmURL+"/health", nil)
		if err != nil {
			jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		req.Header.Set("Authorization", "Bearer "+masterKey)
		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			jsonResp(w, http.StatusBadGateway, map[string]string{"status": "unreachable"})
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
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

	// --- Secrets endpoints ---

	// GET/POST /secrets-config -- manage how secrets are resolved (direct value vs command).
	http.HandleFunc("/secrets-config", func(w http.ResponseWriter, r *http.Request) {
		cfgFile := secretsConfigPath(dataDir)
		switch r.Method {
		case http.MethodOptions:
			cors(w)
			w.WriteHeader(http.StatusNoContent)

		case http.MethodGet:
			data, err := os.ReadFile(cfgFile)
			if err != nil {
				jsonResp(w, http.StatusOK, SecretsConfig{Secrets: map[string]SecretEntry{}})
				return
			}
			cors(w)
			w.Header().Set("Content-Type", "application/json")
			w.Write(data)

		case http.MethodPost:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				jsonResp(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			var cfg SecretsConfig
			if err := json.Unmarshal(body, &cfg); err != nil {
				jsonResp(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
				return
			}
			if err := os.WriteFile(cfgFile, body, 0o600); err != nil {
				jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			jsonResp(w, http.StatusOK, map[string]string{"status": "saved"})

		default:
			jsonResp(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	})

	// GET/POST/DELETE /secrets/<name> -- read/write/delete individual secret values.
	http.HandleFunc("/secrets/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/secrets/")
		if name == "" || strings.Contains(name, "/") || strings.Contains(name, "..") {
			jsonResp(w, http.StatusBadRequest, map[string]string{"error": "invalid secret name"})
			return
		}
		secretFile := filepath.Join(secretsDir(dataDir), name)

		switch r.Method {
		case http.MethodOptions:
			cors(w)
			w.WriteHeader(http.StatusNoContent)

		case http.MethodGet:
			if _, err := os.Stat(secretFile); err != nil {
				jsonResp(w, http.StatusOK, map[string]any{"name": name, "exists": false})
				return
			}
			data, _ := os.ReadFile(secretFile)
			val := strings.TrimSpace(string(data))
			// Return a masked preview (first 4 chars + asterisks).
			masked := mask(val)
			jsonResp(w, http.StatusOK, map[string]any{
				"name":   name,
				"exists": true,
				"masked": masked,
				"length": len(val),
			})

		case http.MethodPost:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				jsonResp(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			var payload struct {
				Value string `json:"value"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				jsonResp(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
				return
			}
			val := strings.TrimSpace(payload.Value)
			if err := os.MkdirAll(secretsDir(dataDir), 0o700); err != nil {
				jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			if err := os.WriteFile(secretFile, []byte(val), 0o600); err != nil {
				jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			jsonResp(w, http.StatusOK, map[string]string{"status": "saved", "name": name})

		case http.MethodDelete:
			os.Remove(secretFile)
			jsonResp(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})

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

// mask returns a masked version of the value (first 4 chars visible).
func mask(s string) string {
	if len(s) <= 4 {
		return strings.Repeat("*", len(s))
	}
	return s[:4] + strings.Repeat("*", len(s)-4)
}
