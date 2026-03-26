package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
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
  cache: true
  cache_params:
    type: redis
    host: redis
    port: 6379

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

// --- Docker socket client ---

const dockerSocket = "/var/run/docker.sock"

// dockerHTTP makes an HTTP request to the Docker Engine API over the Unix socket.
func dockerHTTP(method, path string, body io.Reader) (*http.Response, error) {
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", dockerSocket)
			},
		},
		Timeout: 30 * time.Second,
	}
	req, err := http.NewRequest(method, "http://docker"+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return client.Do(req)
}

// findContainerByService returns the container ID for a compose service name.
func findContainerByService(service string) (string, error) {
	resp, err := dockerHTTP("GET", "/containers/json", nil)
	if err != nil {
		return "", fmt.Errorf("docker API: %w", err)
	}
	defer resp.Body.Close()
	var containers []struct {
		Id     string            `json:"Id"`
		Labels map[string]string `json:"Labels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
		return "", fmt.Errorf("decode containers: %w", err)
	}
	for _, c := range containers {
		if c.Labels["com.docker.compose.service"] == service {
			return c.Id, nil
		}
	}
	return "", fmt.Errorf("container for service %q not found", service)
}

// restartContainer restarts a Docker container by ID.
func restartContainer(id string) error {
	resp, err := dockerHTTP("POST", fmt.Sprintf("/containers/%s/restart", id), nil)
	if err != nil {
		return fmt.Errorf("restart request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("restart failed (%s): %s", resp.Status, string(body))
	}
	return nil
}

// --- Main ---

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

	// --- Normal config-server mode ---

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

	// /cache-ping — proxy to LiteLLM /cache/ping with auth, tests Redis connectivity.
	http.HandleFunc("/cache-ping", func(w http.ResponseWriter, r *http.Request) {
		cors(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		masterKey := "sk-1234"
		if data, err := os.ReadFile(filepath.Join(secretsDir(dataDir), "master_key")); err == nil {
			if mk := strings.TrimSpace(string(data)); mk != "" {
				masterKey = mk
			}
		}
		req, err := http.NewRequest("GET", litellmURL+"/cache/ping", nil)
		if err != nil {
			jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		req.Header.Set("Authorization", "Bearer "+masterKey)
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			jsonResp(w, http.StatusBadGateway, map[string]string{"status": "unreachable", "error": err.Error()})
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
	})

	// /token-check — validate the upstream API token by making a minimal
	// request directly to the configured base_url.  Returns:
	//   {"valid": true}               — token accepted by upstream
	//   {"valid": false, "error": …}  — 401/403 from upstream (expired/invalid)
	//   {"valid": null, "error": …}   — secrets missing or upstream unreachable
	http.HandleFunc("/token-check", func(w http.ResponseWriter, r *http.Request) {
		cors(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		apiKey := readSecret(dataDir, "api_key")
		baseURL := readSecret(dataDir, "base_url")
		if apiKey == "" || baseURL == "" {
			jsonResp(w, http.StatusOK, map[string]any{
				"valid": nil,
				"error": "api_key or base_url secret not configured",
			})
			return
		}

		// Find the first model name from config.yaml to use in the probe.
		model := firstModelFromConfig(*configPath)
		if model == "" {
			jsonResp(w, http.StatusOK, map[string]any{
				"valid": nil,
				"error": "no models configured in config.yaml",
			})
			return
		}

		// POST /chat/completions with empty messages — just enough to trigger auth.
		probeBody := fmt.Sprintf(`{"model":%q,"messages":[],"max_tokens":1}`, model)
		url := strings.TrimRight(baseURL, "/") + "/chat/completions"
		req, err := http.NewRequest("POST", url, strings.NewReader(probeBody))
		if err != nil {
			jsonResp(w, http.StatusOK, map[string]any{"valid": nil, "error": err.Error()})
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+apiKey)

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			jsonResp(w, http.StatusOK, map[string]any{"valid": nil, "error": "upstream unreachable: " + err.Error()})
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)

		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			jsonResp(w, http.StatusOK, map[string]any{
				"valid": false,
				"error": strings.TrimSpace(string(body)),
			})
			return
		}
		// Any other status (200, 400, 422, …) means auth passed.
		jsonResp(w, http.StatusOK, map[string]any{"valid": true})
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

	// --- Docker socket endpoints ---

	// POST /docker/restart — restart a compose service by name.
	// Body: {"service": "litellm"}
	http.HandleFunc("/docker/restart", func(w http.ResponseWriter, r *http.Request) {
		cors(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			jsonResp(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
			return
		}
		var payload struct {
			Service string `json:"service"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Service == "" {
			jsonResp(w, http.StatusBadRequest, map[string]string{"error": "need {\"service\": \"name\"}"})
			return
		}
		id, err := findContainerByService(payload.Service)
		if err != nil {
			jsonResp(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		if err := restartContainer(id); err != nil {
			jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		jsonResp(w, http.StatusOK, map[string]string{"status": "restarted", "service": payload.Service, "container": id[:12]})
	})

	// GET /docker/logs?service=<name>&tail=<n> — fetch container logs via Docker socket.
	http.HandleFunc("/docker/logs", func(w http.ResponseWriter, r *http.Request) {
		cors(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		service := r.URL.Query().Get("service")
		if service == "" {
			jsonResp(w, http.StatusBadRequest, map[string]string{"error": "need ?service=name"})
			return
		}
		tail := r.URL.Query().Get("tail")
		if tail == "" {
			tail = "200"
		}
		id, err := findContainerByService(service)
		if err != nil {
			jsonResp(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		resp, err := dockerHTTP("GET", fmt.Sprintf("/containers/%s/logs?stdout=true&stderr=true&tail=%s", id, tail), nil)
		if err != nil {
			jsonResp(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.Copy(w, resp.Body)
	})

	// GET /docker/containers — list running compose containers (debug helper).
	http.HandleFunc("/docker/containers", func(w http.ResponseWriter, r *http.Request) {
		cors(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		resp, err := dockerHTTP("GET", "/containers/json", nil)
		if err != nil {
			jsonResp(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
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

// readSecret returns the trimmed contents of a named secret file, or "".
func readSecret(dataDir, name string) string {
	data, err := os.ReadFile(filepath.Join(secretsDir(dataDir), name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// firstModelFromConfig extracts the first litellm_params.model value from the
// config YAML.  Uses simple line-scanning (no YAML library) to avoid adding a
// dependency.
func firstModelFromConfig(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	// Look for "model: <value>" lines under litellm_params sections.
	// The pattern is: indented "model:" whose value is NOT a top-level key.
	inParams := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "litellm_params:" {
			inParams = true
			continue
		}
		if inParams && strings.HasPrefix(trimmed, "model:") {
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, "model:"))
			if val != "" {
				return val
			}
		}
		// Reset if we hit a non-indented line (new section).
		if inParams && len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
			inParams = false
		}
	}
	return ""
}

// mask returns a masked version of the value (first 4 chars visible).
func mask(s string) string {
	if len(s) <= 4 {
		return strings.Repeat("*", len(s))
	}
	return s[:4] + strings.Repeat("*", len(s)-4)
}
