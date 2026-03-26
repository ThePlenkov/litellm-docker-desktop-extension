# LiteLLM Docker Desktop Extension — Agent Rules

## Project Overview

Docker Desktop Extension that runs [LiteLLM](https://github.com/BerriAI/litellm) as an OpenAI-compatible proxy with a built-in config editor, secrets management, and health monitoring. This is a **public GitHub project** — no corporate/internal context. Never commit internal URLs, tokens, or company-specific references.

## Architecture

4 Docker Compose services sharing a config volume:

| Service | Image | Port | Role |
|---------|-------|------|------|
| config-server | `${DESKTOP_PLUGIN_IMAGE}` (Go) | 4001 (internal 8080) | Config/secrets CRUD API + Docker socket + token validation |
| litellm | `ghcr.io/berriai/litellm:main-stable` | 4000 | LLM proxy (upstream) |
| postgres | `postgres:15-alpine` | — | LiteLLM database |
| redis | `redis:7-alpine` | — | LiteLLM cache backend |

Plus a **host binary** (`secret-helper`) that runs shell commands on the host machine for fetching secrets from Vault, AWS, etc.

### Key data flow
- UI <-> config-server (port 4001): CRUD for `config.yaml` and secrets
- config-server + litellm share volume `litellm-ext-config` at `/data`
- config-server writes `/data/config.yaml` and `/data/secrets/*`; litellm reads them at startup
- config-server has Docker socket access (`/var/run/docker.sock.raw`) for container management
- `REDIS_HOST=redis` and `REDIS_PORT=6379` are passed to litellm so cache features work out of the box

## Build & Install

**Prerequisites:** Docker Desktop with extensions enabled, pre-built UI (`ui/dist/`).

```bash
# Build the UI first (from ui/ directory)
cd ui && bun install && bun run build && cd ..

# Build Docker image and install extension
make install

# Or to update an already-installed extension
make update
```

### Makefile targets
| Target | What it does |
|--------|-------------|
| `build` | `docker build` the extension image |
| `install` | Build + `docker extension install` |
| `update` | Build + `docker extension update` |
| `remove` | `docker extension rm` |
| `validate` | Build + `docker extension validate` |
| `debug` | `docker extension dev debug` |

## Verification

There is no test suite yet. Verify changes manually:

1. **Build check:** `make build` must succeed
2. **Extension update:** `make update` — reinstalls into Docker Desktop
3. **Health:** Open the extension tab in Docker Desktop — health chip should show "Running"
4. **Config editor:** Configuration tab — Monaco editor loads, schema validation works
5. **Secrets:** Secrets tab — save/reload a secret value
6. **Token check:** After saving an API key, the token validation should report valid/expired within 60s
7. **LiteLLM UI:** Dashboard tab — "Open LiteLLM UI" button works at `http://localhost:4000/ui`

## Tech Stack

- **Backend:** Go (stdlib only, no frameworks) — `backend/main.go`
- **Host binary:** Go — `host/main.go`
- **Frontend:** React 18 + TypeScript + MUI v5 + Vite 6 + Monaco Editor + monaco-yaml
- **Extension API:** `@docker/extension-api-client` + `@docker/docker-mui-theme`

## Config-Server Endpoints

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/health` | GET | Config-server health |
| `/config` | GET/POST | Read/write `config.yaml` |
| `/litellm-health` | GET | Proxy to LiteLLM `/health/liveliness` |
| `/litellm-model-health` | GET | Proxy to LiteLLM `/health` with auth (per-model status) |
| `/cache-ping` | GET | Proxy to LiteLLM `/cache/ping` (Redis connectivity) |
| `/token-check` | GET | Validate upstream API token directly (bypasses LiteLLM) |
| `/secrets-config` | GET/POST | Manage secret resolution modes (direct vs command) |
| `/secrets/<name>` | GET/POST/DELETE | Read/write/delete individual secret values |
| `/docker/restart` | POST | Restart a compose service by name |
| `/docker/containers` | GET | List running compose containers |
| `/docker/logs` | GET | Fetch container logs via Docker socket |

## Code Patterns

- **No external Go dependencies** — backend and host use only stdlib
- **Frontend URLs are hardcoded:** `CONFIG_URL = 'http://localhost:4001'`, `PROXY_URL = 'http://localhost:4000'`
- **Container restart via Docker socket:** config-server uses `/var/run/docker.sock.raw` (Docker Desktop VM socket path) with `findContainerByService()` and `restartContainer()` helpers
- **Secret resolution:** Two modes — "direct" (literal value) and "command" (shell command run via host binary)
- **Auth monitor is notification-only:** `useAuthMonitor` detects credential errors and token expiry but NEVER restarts LiteLLM or auto-refreshes secrets. User must act manually via the Secrets tab.
- **Token validation:** `/token-check` sends a minimal `POST /chat/completions` (empty messages, zero token cost) directly to the upstream API. Returns 401/403 = expired, anything else = valid.
- **CORS:** `Access-Control-Allow-Origin: *` on all config-server endpoints (by design for local extension)
- **Default credentials:** master key `sk-1234`, UI password `sk-1234`, Postgres password `litellm-internal-db`

## File Map

```
backend/main.go       — config-server: HTTP endpoints, Docker socket client, token validation
host/main.go          — secret-helper: runs shell commands on host
ui/src/App.tsx        — Root: 3 tabs + health chip
ui/src/ConfigTab.tsx  — Monaco YAML editor with LiteLLM schema
ui/src/DashboardTab.tsx — Links to LiteLLM UI/API, secret refresh button
ui/src/SecretsTab.tsx — CRUD for api_key, base_url, master_key
ui/src/useAuthMonitor.ts — Notification-only: token expiry + per-model auth error toasts
ui/src/useHealthCheck.ts — Poll /health/liveliness every 5s
ui/src/litellm-schema.json — JSON Schema for config validation
compose.yaml          — 4 services + 3 named volumes
metadata.json         — Docker Desktop extension manifest
Dockerfile            — 3-stage build (host-builder, backend-builder, scratch + CA certs)
```

## LiteLLM Health Check Notes

- `/health/liveliness` — lightweight, no LLM calls, just checks if the process is up
- `/health` (with auth) — makes real API calls to each configured model to verify connectivity
- The health probe sends `max_tokens=1` by default, which some models reject with `BadRequestError`
- **Fix:** set `health_check_max_tokens: 10` (or higher) under `model_info` for affected models:
  ```yaml
  model_list:
    - model_name: my-model
      litellm_params:
        model: openai/my-model
        api_key: os.environ/MY_API_KEY
      model_info:
        mode: chat
        health_check_max_tokens: 10   # prevents false-negative health checks
  ```
- Other useful per-model health params: `health_check_model` (for wildcards), `health_check_timeout`

## Known Gotchas

- **UI must be pre-built** before `docker build` — the Dockerfile does NOT build the frontend
- **No test infrastructure** — verify manually via Docker Desktop
- **Dockerfile is `FROM scratch`** — CA certificates are explicitly copied from the Alpine build stage. Without them, the config-server cannot make HTTPS calls (e.g. `/token-check` fails with `x509: certificate signed by unknown authority`).
- **Docker socket path:** Docker Desktop VM uses `/var/run/docker.sock.raw`, not `/var/run/docker.sock`
- LiteLLM's `/health/liveliness` is intentionally misspelled (matches upstream API)
- `@monaco-editor/react` is in package.json but unused — raw `monaco-editor` is used directly via `monaco-yaml`
- **Never auto-restart LiteLLM from the auth monitor.** A previous version did this and caused an infinite restart loop: auth errors detected → auto-refresh secrets → restart → same errors → repeat. The auth monitor must be notification-only.
- **Non-recoverable vs recoverable auth errors:** "does not have access to" (model-level permission) is different from "Invalid token" (expired credential). Only the latter is fixable by refreshing secrets. The UI should distinguish these in notifications.
- **Model API version pinning:** When configuring an OpenAI-compatible proxy endpoint as `api_base`, do NOT pin to a specific app version (e.g. `/app:12`). Use the unversioned URL so models are always served from the latest version.
- **Docker Compose native `secrets:` cannot be used here.** Three reasons: (1) Compose secrets are static (resolved at `docker compose up` time), but our secrets are written at runtime by config-server and refreshed by the host binary — updating a compose secret requires recreating all services; (2) `file:` source paths are host-relative, but our secrets live inside the `litellm-ext-config` Docker volume with no stable host path, and the extension framework manages the compose lifecycle; (3) LiteLLM doesn't support the `_FILE` env var convention (like MySQL/Postgres do), so we'd still need the shell entrypoint to `cat` files and `export` them as env vars. The current design (shared volume + shell entrypoint) is the correct pattern for runtime-writable secrets in a Docker Desktop extension.
