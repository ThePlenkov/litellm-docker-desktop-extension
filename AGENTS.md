# LiteLLM Docker Desktop Extension — Agent Rules

## Project Overview

Docker Desktop Extension that runs [LiteLLM](https://github.com/BerriAI/litellm) as an OpenAI-compatible proxy with a built-in config editor, secrets management, and health monitoring. This is a **public GitHub project** — no corporate/internal context.

## Architecture

4 Docker Compose services sharing a config volume:

| Service | Image | Port | Role |
|---------|-------|------|------|
| config-server | `${DESKTOP_PLUGIN_IMAGE}` (Go) | 4001 (internal 8080) | Config/secrets CRUD API |
| litellm | `ghcr.io/berriai/litellm:main-stable` | 4000 | LLM proxy (upstream) |
| postgres | `postgres:15-alpine` | — | LiteLLM database |
| redis | `redis:7-alpine` | — | LiteLLM cache backend |

Plus a **host binary** (`secret-helper`) that runs shell commands on the host machine for fetching secrets from Vault, AWS, etc.

### Key data flow
- UI <-> config-server (port 4001): CRUD for `config.yaml` and secrets
- config-server + litellm share volume `litellm-ext-config` at `/data`
- config-server writes `/data/config.yaml` and `/data/secrets/*`; litellm reads them at startup
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
6. **LiteLLM UI:** Dashboard tab — "Open LiteLLM UI" button works at `http://localhost:4000/ui`

## Tech Stack

- **Backend:** Go (stdlib only, no frameworks) — `backend/main.go`
- **Host binary:** Go — `host/main.go`
- **Frontend:** React 18 + TypeScript + MUI v5 + Vite 6 + Monaco Editor + monaco-yaml
- **Extension API:** `@docker/extension-api-client` + `@docker/docker-mui-theme`

## Code Patterns

- **No external Go dependencies** — backend and host use only stdlib
- **Frontend URLs are hardcoded:** `CONFIG_URL = 'http://localhost:4001'`, `PROXY_URL = 'http://localhost:4000'`
- **Container restart pattern:** Find container by `Image.includes('litellm') && !Image.includes('config-server')`, then `docker container restart <id>` — duplicated in ConfigTab, DashboardTab, SecretsTab, useAuthMonitor
- **Secret resolution:** Two modes — "direct" (literal value) and "command" (shell command run via host binary)
- **CORS:** `Access-Control-Allow-Origin: *` on all config-server endpoints (by design for local extension)
- **Default credentials:** master key `sk-1234`, UI password `sk-1234`, Postgres password `litellm-internal-db`

## File Map

```
backend/main.go       — config-server: 7 HTTP endpoints, default config template
host/main.go          — secret-helper: runs shell commands on host
ui/src/App.tsx        — Root: 3 tabs + health chip
ui/src/ConfigTab.tsx  — Monaco YAML editor with LiteLLM schema
ui/src/DashboardTab.tsx — Links to LiteLLM UI/API, secret refresh button
ui/src/SecretsTab.tsx — CRUD for api_key, base_url, master_key
ui/src/useAuthMonitor.ts — Auto-detect auth errors, auto-refresh secrets
ui/src/useHealthCheck.ts — Poll /health/liveliness every 5s
ui/src/litellm-schema.json — 908-line JSON Schema for config validation
compose.yaml          — 4 services + 3 named volumes
metadata.json         — Docker Desktop extension manifest
Dockerfile            — 3-stage build (host-builder, backend-builder, scratch)
```

## Known Gotchas

- **UI must be pre-built** before `docker build` — the Dockerfile does NOT build the frontend
- **No test infrastructure** — verify manually via Docker Desktop
- LiteLLM's `/health/liveliness` is intentionally misspelled (matches upstream API)
- `@monaco-editor/react` is in package.json but unused — raw `monaco-editor` is used directly via `monaco-yaml`
