---
name: litellm-docker-extension
description: >
  Install, configure, and use the LiteLLM Docker Desktop Extension —
  an OpenAI-compatible LLM proxy running inside Docker Desktop with a
  built-in YAML config editor, secrets management, health monitoring,
  and Redis caching. Use when the user asks about LiteLLM extension
  setup, configuration, troubleshooting, or development workflow.
triggers:
  - litellm extension
  - litellm docker desktop
  - litellm proxy setup
  - config editor
  - extension install
  - extension update
---

# LiteLLM Docker Desktop Extension

## What It Is

A Docker Desktop Extension that runs [LiteLLM](https://github.com/BerriAI/litellm) — an OpenAI-compatible proxy that lets you call 100+ LLM providers (OpenAI, Anthropic, Azure, Ollama, etc.) through a single unified API.

The extension bundles:
- **LiteLLM proxy** on port 4000 (OpenAI-compatible API)
- **Config server** on port 4001 (Go HTTP API for config/secrets management)
- **PostgreSQL** for LiteLLM's database
- **Redis** for response caching
- **React UI** inside Docker Desktop with Monaco YAML editor
- **Host binary** (`secret-helper`) for fetching secrets from host-side tools

## Prerequisites

- Docker Desktop 4.8+ with extensions enabled
- Node.js / Bun (for building the UI)
- Go 1.24+ (for building backend — compiled inside Docker)

## Installation

### Quick install (from source)

```bash
git clone <repo-url> litellm-docker-desktop-extension
cd litellm-docker-desktop-extension

# 1. Build the frontend
cd ui && bun install && bun run build && cd ..

# 2. Build and install the extension
make install
```

### Update after changes

```bash
# Rebuild UI if frontend changed
cd ui && bun run build && cd ..

# Rebuild image and update extension
make update
```

### Remove

```bash
make remove
```

## Configuration

### Adding LLM Models

Open the extension in Docker Desktop, go to the **Configuration** tab, and edit the YAML. The Monaco editor provides autocomplete and validation via JSON Schema.

Example config:

```yaml
model_list:
  - model_name: gpt-4
    litellm_params:
      model: openai/gpt-4
      api_key: os.environ/LITELLM_API_KEY

  - model_name: claude-sonnet
    litellm_params:
      model: anthropic/claude-sonnet-4-20250514
      api_key: os.environ/LITELLM_API_KEY

  - model_name: llama3
    litellm_params:
      model: ollama/llama3
      api_base: http://host.docker.internal:11434

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
```

Click **Save & Restart** to apply.

### Enabling Redis Cache

Redis runs as a sidecar and is pre-configured via `REDIS_HOST=redis` and `REDIS_PORT=6379` environment variables. To enable caching, add to your config:

```yaml
litellm_settings:
  cache: true
  cache_params:
    type: redis
    host: redis
    port: 6379
```

Or simply enable it from the LiteLLM admin UI at `http://localhost:4000/ui` — it will pick up the Redis host from the environment.

### Managing Secrets

Go to the **Secrets** tab. Three secrets are supported:

| Secret | Env Var | Purpose |
|--------|---------|---------|
| `api_key` | `LITELLM_API_KEY` | API key for upstream LLM providers |
| `base_url` | `LITELLM_BASE_URL` | Custom base URL for providers |
| `master_key` | `LITELLM_MASTER_KEY` | Auth key for LiteLLM proxy itself |

Each secret supports two modes:
- **Direct value**: Paste the secret directly
- **Host command**: A shell command that runs on your host machine (e.g., `vault kv get -field=key secret/openai`, `aws secretsmanager get-secret-value ...`, `pass show openai/key`)

Command-mode secrets are auto-refreshed when the auth monitor detects authentication failures.

### Environment Variables

Set these before `make install` or in your shell:

| Variable | Default | Description |
|----------|---------|-------------|
| `LITELLM_MASTER_KEY` | `sk-1234` | Master key for LiteLLM proxy auth |
| `LITELLM_API_KEY` | (empty) | API key for upstream LLM providers |
| `LITELLM_BASE_URL` | (empty) | Custom provider base URL |
| `UI_USERNAME` | `admin` | LiteLLM admin UI username |
| `UI_PASSWORD` | `sk-1234` | LiteLLM admin UI password |

## Usage

### As an OpenAI-compatible API

```bash
curl http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer sk-1234" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

### Admin UI

Click **Open LiteLLM UI** on the Dashboard tab, or visit `http://localhost:4000/ui` directly. Login with the UI username/password (default: `admin` / `sk-1234`).

### Health Monitoring

- The extension header shows a real-time health chip (Running / Starting...)
- The auth monitor polls model health every 30s and shows toast notifications for auth failures
- Command-mode secrets are auto-refreshed on auth errors

## Development Workflow

### Project structure

```
backend/main.go       -- Config server (Go, 7 HTTP endpoints)
host/main.go          -- Secret helper host binary (Go)
ui/src/               -- React + MUI + Monaco frontend
  App.tsx             -- Root: 3 tabs + health chip
  ConfigTab.tsx       -- Monaco YAML editor with schema validation
  DashboardTab.tsx    -- Links + secret refresh
  SecretsTab.tsx      -- Secret CRUD (3 fields, 2 modes)
  useAuthMonitor.ts   -- Auto-detect auth errors, auto-refresh
  useHealthCheck.ts   -- Poll /health/liveliness every 5s
  litellm-schema.json -- JSON Schema for config autocomplete
compose.yaml          -- 4 services, 3 named volumes
Dockerfile            -- 3-stage build (host, backend, scratch)
metadata.json         -- Docker Desktop extension manifest
Makefile              -- build/install/update/remove/validate/debug
```

### Build cycle

```bash
# Frontend dev (hot reload outside Docker Desktop)
cd ui && bun run dev

# Full rebuild + extension update
cd ui && bun run build && cd .. && make update

# Debug mode (extension dev tools)
make debug
```

### Makefile targets

| Target | Description |
|--------|-------------|
| `make build` | Build Docker image |
| `make install` | Build + install extension |
| `make update` | Build + update running extension |
| `make remove` | Remove extension |
| `make validate` | Build + validate extension metadata |
| `make debug` | Enable extension dev debug mode |

## Troubleshooting

### "Cache connection test failed: Either 'host' or 'url' must be specified for redis"
Redis connection env vars (`REDIS_HOST=redis`, `REDIS_PORT=6379`) are set in `compose.yaml`. If you see this error, ensure you're running the latest build with `make update`.

### Extension health shows "Starting..." indefinitely
- Check Docker Desktop logs for the litellm container
- Verify Postgres is healthy: the litellm service depends on it
- Check that config.yaml is valid YAML (use the Configuration tab editor)

### Auth errors on model health checks
- Go to Secrets tab and verify `api_key` is set correctly
- If using command mode, test the command locally first
- The auth monitor will auto-refresh command-mode secrets

### UI shows blank/white screen
- Ensure `ui/dist/` was built before `docker build`
- Run `cd ui && bun run build` then `make update`
