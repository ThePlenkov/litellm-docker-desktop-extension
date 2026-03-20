# syntax=docker/dockerfile:1

# Stage 1: Build the React UI
FROM --platform=$BUILDPLATFORM node:22-alpine AS ui-builder
WORKDIR /app
COPY ui/package.json ui/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY ui/ .
RUN npm run build

# Stage 2: Extension image
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
COPY --from=ui-builder /app/dist ./ui
