# LiteLLM — Docker Desktop Extension

Docker Desktop extension that runs [LiteLLM](https://github.com/BerriAI/litellm) proxy with a built-in YAML config editor.

## What it does

- Starts the latest **stable** LiteLLM proxy (`ghcr.io/berriai/litellm:main-stable`) on port `4000`
- Shows a live health indicator inside Docker Desktop
- **Built-in YAML config editor** — edit `config.yaml` right in the extension and apply changes
- **Open LiteLLM UI** button launches the admin dashboard in your browser
- **Open API Docs** button opens the Swagger/OpenAPI page

## Install

```bash
docker build --tag=litellm/docker-desktop-extension:latest .
docker extension install litellm/docker-desktop-extension:latest
```

Or use the Makefile:

```bash
make install
```

## Configuration

### Config editor (recommended)

Open the **Configuration** tab in the extension to edit the LiteLLM proxy config directly as YAML.
The editor supports the full [LiteLLM proxy config format](https://docs.litellm.ai/docs/proxy/configs):

- **`model_list`** — define models, providers, and API keys
- **`litellm_settings`** — library settings (retries, timeouts, caching)
- **`general_settings`** — proxy settings (master key, CORS, alerting)
- **`router_settings`** — load balancing and fallback routing
- **`environment_variables`** — set env vars at startup

Click **Save** to persist changes, or **Save & Restart** to apply them immediately.

Example config:

```yaml
model_list:
  - model_name: gpt-4
    litellm_params:
      model: openai/gpt-4-turbo
      api_key: os.environ/OPENAI_API_KEY

  - model_name: claude-sonnet
    litellm_params:
      model: anthropic/claude-sonnet-4-20250514
      api_key: os.environ/ANTHROPIC_API_KEY

  - model_name: llama3
    litellm_params:
      model: ollama/llama3
      api_base: http://host.docker.internal:11434

litellm_settings:
  drop_params: true
  num_retries: 3
  request_timeout: 600

general_settings:
  master_key: os.environ/LITELLM_MASTER_KEY
```

### Environment variables

| Variable | Default | Description |
|---|---|---|
| `LITELLM_MASTER_KEY` | `sk-1234` | Admin/master key for the proxy |

To add LLM provider keys, either use the `environment_variables` section in the config editor, or edit `compose.yaml`:

```yaml
services:
  litellm:
    environment:
      - LITELLM_MASTER_KEY=sk-your-key
      - OPENAI_API_KEY=sk-...
      - ANTHROPIC_API_KEY=sk-ant-...
```

Then rebuild:

```bash
make update
```

## Usage

After installation, open Docker Desktop and look for **LiteLLM** in the left sidebar.

The proxy is available at `http://localhost:4000` and is OpenAI-compatible:

```bash
curl http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer sk-1234" \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4", "messages": [{"role": "user", "content": "hello"}]}'
```

## Uninstall

```bash
make remove
# or
docker extension rm litellm/docker-desktop-extension:latest
```

## Project structure

```
.
├── Dockerfile          # Extension image (builds host + backend binaries, FROM scratch)
├── Makefile            # Build/install/update shortcuts
├── compose.yaml        # LiteLLM + config-server + Postgres + Redis services
├── icon.svg            # Extension icon
├── metadata.json       # Docker Desktop extension metadata
├── backend/
│   ├── go.mod          # Config-server Go module
│   └── main.go         # Config-server: HTTP API for config & secrets
├── host/
│   ├── go.mod          # Secret-helper Go module
│   └── main.go         # Host binary for running shell commands
└── ui/
    └── src/            # React + MUI extension dashboard
```

## Architecture

The extension runs four services (defined in `compose.yaml`):

1. **litellm** — the LiteLLM proxy (`ghcr.io/berriai/litellm:main-stable`), reading config from a shared Docker volume
2. **config-server** — a Go HTTP server (built into the extension image via `${DESKTOP_PLUGIN_IMAGE}`) that reads/writes `config.yaml` and secrets on the shared volume, and proxies authenticated health checks to LiteLLM
3. **postgres** — PostgreSQL database for LiteLLM
4. **redis** — Redis cache

The config-server binary is compiled in the main `Dockerfile` and embedded in the extension image. The compose service references it via `${DESKTOP_PLUGIN_IMAGE}` — no separate image build required.

Config is persisted in a Docker named volume (`litellm-config`), so it survives container restarts.

## License

MIT
