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

// --- Secret-test mode ---

// runSecretTestServer starts a minimal HTTP server that reads/writes a test
// secret from the shared volume. Used to verify Docker secrets plumbing.
func runSecretTestServer(dataDir, addr string) {
	secretFile := filepath.Join(dataDir, "secrets", "test_secret")

	// Seed a mock value if none exists.
	os.MkdirAll(filepath.Dir(secretFile), 0o700)
	if _, err := os.Stat(secretFile); err != nil {
		os.WriteFile(secretFile, []byte("mock-secret-initial-value"), 0o600)
		log.Printf("[secret-test] seeded default test_secret")
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/secret", func(w http.ResponseWriter, r *http.Request) {
		cors(w)
		switch r.Method {
		case http.MethodOptions:
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			data, err := os.ReadFile(secretFile)
			if err != nil {
				jsonResp(w, http.StatusOK, map[string]string{"value": "", "error": "not found"})
				return
			}
			jsonResp(w, http.StatusOK, map[string]string{"value": strings.TrimSpace(string(data))})
		case http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			var payload struct {
				Value string `json:"value"`
			}
			if json.Unmarshal(body, &payload) != nil {
				// Treat raw body as the value.
				payload.Value = strings.TrimSpace(string(body))
			}
			os.MkdirAll(filepath.Dir(secretFile), 0o700)
			if err := os.WriteFile(secretFile, []byte(strings.TrimSpace(payload.Value)), 0o600); err != nil {
				jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			jsonResp(w, http.StatusOK, map[string]string{"status": "saved"})
		default:
			jsonResp(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		jsonResp(w, http.StatusOK, map[string]string{"status": "ok", "mode": "secret-test"})
	})

	log.Printf("[secret-test] listening on %s, secret at %s", addr, secretFile)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// --- Main ---

func main() {
	configPath := flag.String("config", envOr("CONFIG_PATH", "/data/config.yaml"), "path to config.yaml")
	addr := flag.String("addr", ":"+envOr("PORT", "8080"), "listen address")
	healthCheck := flag.Bool("health-check", false, "run health check and exit")
	mode := flag.String("mode", "server", "run mode: server (default) or secret-test")
	flag.Parse()

	if *healthCheck {
		resp, err := http.Get("http://localhost" + *addr + "/health")
		if err != nil || resp.StatusCode != 200 {
			os.Exit(1)
		}
		os.Exit(0)
	}

	dataDir := filepath.Dir(*configPath)

	// Secret-test mode: simple secret reader/writer for PoC validation.
	if *mode == "secret-test" {
		runSecretTestServer(dataDir, *addr)
		return
	}

	// --- Normal config-server mode ---

	ensureDefault(*configPath)
	os.MkdirAll(secretsDir(dataDir), 0o700)

	litellmURL := envOr("LITELLM_URL", "http://litellm:4000")
	secretTestURL := envOr("SECRET_TEST_URL", "http://secret-test:8080")

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
	// Body: {"service": "litellm"} or {"service": "secret-test"}
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

	// GET /docker/test-secret — read the test secret via the secret-test service (verify round-trip).
	http.HandleFunc("/docker/test-secret", func(w http.ResponseWriter, r *http.Request) {
		cors(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodPost {
			// Write a test secret to the shared volume, then verify via secret-test service.
			body, _ := io.ReadAll(r.Body)
			var payload struct {
				Value string `json:"value"`
			}
			if json.Unmarshal(body, &payload) != nil || payload.Value == "" {
				jsonResp(w, http.StatusBadRequest, map[string]string{"error": "need {\"value\": \"...\"}"})
				return
			}
			secretFile := filepath.Join(secretsDir(dataDir), "test_secret")
			if err := os.MkdirAll(secretsDir(dataDir), 0o700); err != nil {
				jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			if err := os.WriteFile(secretFile, []byte(payload.Value), 0o600); err != nil {
				jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			// Now read back from the secret-test service to confirm it sees the change.
			client := &http.Client{Timeout: 5 * time.Second}
			verifyResp, err := client.Get(secretTestURL + "/secret")
			if err != nil {
				jsonResp(w, http.StatusOK, map[string]any{
					"written": payload.Value,
					"verify":  "secret-test service unreachable: " + err.Error(),
					"match":   false,
				})
				return
			}
			defer verifyResp.Body.Close()
			var result map[string]string
			json.NewDecoder(verifyResp.Body).Decode(&result)
			jsonResp(w, http.StatusOK, map[string]any{
				"written": payload.Value,
				"readback": result["value"],
				"match":   payload.Value == result["value"],
				"source":  "secret-test",
			})
			return
		}
		// GET: just proxy to the secret-test service.
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get(secretTestURL + "/secret")
		if err != nil {
			jsonResp(w, http.StatusBadGateway, map[string]string{"error": "secret-test unreachable: " + err.Error()})
			return
		}
		defer resp.Body.Close()
		var result map[string]string
		json.NewDecoder(resp.Body).Decode(&result)
		jsonResp(w, http.StatusOK, map[string]any{
			"value":  result["value"],
			"source": "secret-test",
		})
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
