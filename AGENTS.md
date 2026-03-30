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
backend/prompt_compressor.py — Python callback for context compression (go:embed)
backend/tools_compressor.py  — Python callback for tools compression (go:embed)
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

## Prompt Compressor (Context Summarisation Callback)

A custom LiteLLM `async_pre_call_hook` that automatically summarises old conversation history when the token count exceeds a configurable fraction of the model's context window. This reduces token usage for long-running agent sessions without losing important context.

### How it works
1. On every `/chat/completions` request, the callback counts tokens in `messages[]`
2. If tokens exceed `COMPRESSOR_THRESHOLD` (default 60%) of the model's max input, it triggers
3. Messages are split into: system messages (kept), old messages (summarised), recent messages (kept intact)
4. The split respects tool-call / tool-response chains — they are never broken apart
5. Old messages are summarised by a cheap model (default `gpt-4o-mini`) in a single call
6. The summary replaces the old messages; the request proceeds with a much smaller context

### Enabling
In `config.yaml`, uncomment under `litellm_settings` and add `callback_settings`:
```yaml
litellm_settings:
  callbacks:
    - prompt_compressor.proxy_handler_instance

callback_settings:
  prompt_compressor:
    model: gpt-4o-mini
    threshold: 0.6
    min_tokens: 10000
    min_messages: 10
    keep_recent: 5
    summary_ratio: 0.1
```

### Configuration (via `callback_settings` in config.yaml)
| Key | Default | Description |
|-----|---------|-------------|
| `model` | `gpt-4o-mini` | Model used for the summarisation call |
| `threshold` | `0.6` | Fraction of context window that triggers compression |
| `min_tokens` | `10000` | Absolute token floor — skip compression if total tokens are below this |
| `min_messages` | `10` | Minimum non-system messages before compression triggers |
| `keep_recent` | `5` | Minimum number of recent messages always preserved |
| `summary_ratio` | `0.1` | Summary target as fraction of old messages' token count (min 100 tokens) |

### Deployment mechanism
- `backend/prompt_compressor.py` is embedded via `//go:embed` in `backend/main.go`
- Config-server writes the embedded content to `/data/prompt_compressor.py` on every startup
- `PYTHONPATH=/data` on the litellm container makes it importable
- LiteLLM discovers `proxy_handler_instance` in the module

### Thinking/reasoning token handling
- `litellm.token_counter()` silently skips `thinking_blocks` (list-of-dict) but counts `reasoning_content` (str)
- The compressor estimates thinking block tokens (~4 chars/token) and adds them to the count for threshold checks
- Before summarization, `reasoning_content` and `thinking_blocks` are stripped from old messages — the model's internal scratchpad is not useful context and can be the majority of the token cost
- Recent messages (kept intact) retain their thinking blocks — required by Anthropic's API for multi-turn

## Tools Compressor (Tool Definition Compression Callback)

A custom LiteLLM `async_pre_call_hook` that reduces token usage from the `tools[]` array in chat/completions requests. Agent sessions with 50-100+ tools can spend 10-40K tokens per request just on tool definitions — before any conversation starts.

### How it works
1. On every `/chat/completions` request, checks the `tools[]` array
2. If the number of tools exceeds `min_tools` (default 5), compression triggers
3. **Phase 1 (rule-based, zero cost, always on):**
   - Strips `description` from all parameter schemas recursively
   - Truncates tool-level descriptions to `max_desc` chars (default 200)
   - Removes examples, markdown fences, and trailing noise from descriptions
4. **Phase 2 (LLM-based, optional):**
   - Rewrites remaining long descriptions as concise one-liners using a cheap model
   - Only runs when `use_llm: true` is set in callback_settings
5. Compressed tool sets are cached by content hash — tools that don't change between requests are never reprocessed

### Enabling
In `config.yaml`, add under `litellm_settings` and `callback_settings`:
```yaml
litellm_settings:
  callbacks:
    - tools_compressor.proxy_handler_instance      # reduces tool definition tokens
    - prompt_compressor.proxy_handler_instance     # reduces conversation history tokens (optional)

callback_settings:
  tools_compressor:
    min_tools: 5
    max_desc: 200
    strip_param_desc: true
```

### Configuration (via `callback_settings` in config.yaml)
| Key | Default | Description |
|-----|---------|-------------|
| `min_tools` | `5` | Minimum tool count to trigger compression |
| `max_desc` | `200` | Max chars per tool description after rule-based truncation |
| `strip_param_desc` | `true` | Strip parameter descriptions from schemas |
| `use_llm` | `false` | Enable LLM-based Phase 2 description rewriting |
| `llm_model` | `gpt-4o-mini` | Model for LLM compression (Phase 2 only) |
| `llm_max_desc` | `80` | Max chars per description after LLM rewrite |

### Deployment mechanism
- `backend/tools_compressor.py` is embedded via `//go:embed` in `backend/main.go`
- Config-server writes the embedded content to `/data/tools_compressor.py` on every startup
- `PYTHONPATH=/data` on the litellm container makes it importable
- LiteLLM discovers `proxy_handler_instance` in the module

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
- **Prompt Compressor callback must use `metadata={"_prompt_compressor_internal": True}`** on its own summarisation call to prevent infinite recursion through the same `async_pre_call_hook`.
- **Tools Compressor callback must use `metadata={"_tools_compressor_internal": True}`** on its own LLM calls (Phase 2) to prevent infinite recursion through the same `async_pre_call_hook`.
- **Custom callback format in `config.yaml`:** LiteLLM resolves custom callbacks via `get_instance_fn()` which splits by `.` — last part is the instance name, rest is the module path. Use `module.proxy_handler_instance` (e.g. `prompt_compressor.proxy_handler_instance`), NOT bare module names like `prompt_compressor` (resolves to empty module path → crash). The config file is at `/data/config.yaml` so LiteLLM looks for `/data/<module>.py`.
- **`call_type` in `async_pre_call_hook` is `"acompletion"`, NOT `"completion"`:** LiteLLM proxy routes all requests through async code paths. Custom callbacks must check `call_type not in ("completion", "acompletion")` — checking only `"completion"` will cause the hook to silently skip every proxy request.
- **Custom callbacks must use `llm_router` for internal LLM calls:** Direct `litellm.acompletion()` inside a callback does NOT have access to API keys configured in `config.yaml`. Import `llm_router` from `litellm.proxy.proxy_server` and use `llm_router.acompletion()` instead. Fall back to `litellm.acompletion()` only for standalone (non-proxy) usage.
- **`litellm.get_model_info()` returns built-in DB values, not config values:** For models like `openai/ml-asset:static-model/gpt-5-nano`, it may return wrong `max_input_tokens` from litellm's internal model database rather than the `model_info` in `config.yaml`. Callbacks should treat `get_model_info` failures as non-fatal and fall back to a default (e.g. 128000).
- **`callbacks/` directory does not exist** — the canonical source for both callback files is `backend/`. They are embedded via `//go:embed` in `backend/main.go` and written to `/data/` at runtime by config-server.
- **Rapid iteration on callbacks:** `make update` may not restart containers with new embedded files if Docker cache hits. For development, write Python files directly to the volume via `docker exec` on the litellm container, then restart litellm via `POST /docker/restart`. For production builds, use `docker build --no-cache`.
- **LiteLLM `general_settings` bug (v1.82.x):** `ProxyConfig.load_config()` does NOT declare `general_settings` in its `global` statement, so `general_settings = config.get(...)` creates a local variable. However, the caller at `proxy_server.py:828` assigns the return value back to the module-level `general_settings`, so it works at startup. But the initial in-memory dict is empty if you test from a separate Python process — don't be misled by `python3 -c` checks inside the container.
- **`store_prompts_in_spend_logs` needs env var fallback:** Despite being in `config.yaml`'s `general_settings`, this setting may not survive config reloads (the DB's `LiteLLM_Config` table can override it). Set `STORE_PROMPTS_IN_SPEND_LOGS=true` as a container env var for reliability. Without it, `proxy_server_request`, `messages`, and `response` columns in `LiteLLM_SpendLogs` will all be `{}`.
- **`messages` column only populated for `_arealtime` calls (v1.82.x):** `_get_messages_for_spend_logs_payload` in `spend_tracking_utils.py` has a `call_type == "_arealtime"` filter — regular `acompletion` calls get `{}` for messages even with `store_prompts_in_spend_logs=true`. Request data is instead stored in the `proxy_server_request` column (full request body). The LiteLLM UI reads from both columns.
