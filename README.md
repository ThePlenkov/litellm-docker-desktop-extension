# LiteLLM — Docker Desktop Extension

Minimalistic Docker Desktop extension that runs [LiteLLM](https://github.com/BerriAI/litellm) proxy and gives you one-click access to its UI.

## What it does

- Starts the latest **stable** LiteLLM proxy (`docker.litellm.ai/berriai/litellm:main-stable`) on port `4000`
- Shows a live health indicator inside Docker Desktop
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

| Variable | Default | Description |
|---|---|---|
| `LITELLM_MASTER_KEY` | `sk-1234` | Admin/master key for the proxy |

Set environment variables before installing, or edit `compose.yaml` and run `make update`.

To add LLM provider keys (OpenAI, Anthropic, etc.), edit `compose.yaml` and add them under `environment`:

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

After installation, open Docker Desktop → look for **LiteLLM** in the left sidebar.

The proxy is available at `http://localhost:4000` and is OpenAI-compatible:

```bash
curl http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer sk-1234" \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4o", "messages": [{"role": "user", "content": "hello"}]}'
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
├── Dockerfile          # Extension image (FROM scratch)
├── Makefile            # Build/install/update shortcuts
├── compose.yaml        # LiteLLM service definition
├── icon.svg            # Extension icon
├── metadata.json       # Docker Desktop extension metadata
└── ui/
    └── index.html      # Extension dashboard (vanilla HTML/CSS/JS)
```

## License

MIT
