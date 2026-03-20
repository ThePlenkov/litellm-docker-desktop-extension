# syntax=docker/dockerfile:1

# Stage 1: Build the host binary (secret-helper) for all platforms
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS host-builder
WORKDIR /src
COPY host/go.mod host/main.go ./
RUN CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -ldflags="-s -w" -o /out/darwin/secret-helper  . && \
    CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -ldflags="-s -w" -o /out/darwin-arm64/secret-helper  . && \
    CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -ldflags="-s -w" -o /out/linux/secret-helper   . && \
    CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -ldflags="-s -w" -o /out/linux-arm64/secret-helper   . && \
    CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o /out/windows/secret-helper.exe .

# Stage 3: Extension image
FROM scratch

LABEL org.opencontainers.image.title="LiteLLM" \
      org.opencontainers.image.description="Run LiteLLM proxy from Docker Desktop. OpenAI-compatible gateway for 100+ LLMs." \
      org.opencontainers.image.vendor="LiteLLM Community" \
      com.docker.desktop.extension.api.version=">= 0.3.4" \
      com.docker.desktop.extension.icon="icon.svg" \
      com.docker.extension.screenshots="" \
      com.docker.extension.detailed-description="Docker Desktop extension that runs the latest stable LiteLLM proxy with a built-in YAML config editor, health dashboard, and quick access to its UI and API docs." \
      com.docker.extension.publisher-url="https://github.com/berriai/litellm" \
      com.docker.extension.changelog=""

COPY metadata.json .
COPY icon.svg .
COPY compose.yaml .
COPY ui/dist ./ui
COPY --from=host-builder /out/darwin  /host/darwin
COPY --from=host-builder /out/linux   /host/linux
COPY --from=host-builder /out/windows /host/windows
