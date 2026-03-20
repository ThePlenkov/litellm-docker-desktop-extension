# syntax=docker/dockerfile:1
FROM scratch

LABEL org.opencontainers.image.title="LiteLLM" \
      org.opencontainers.image.description="Run LiteLLM proxy from Docker Desktop. OpenAI-compatible gateway for 100+ LLMs." \
      org.opencontainers.image.vendor="LiteLLM Community" \
      com.docker.desktop.extension.api.version="0.3.4" \
      com.docker.desktop.extension.icon="icon.svg" \
      com.docker.extension.screenshots="" \
      com.docker.extension.detailed-description="Minimalistic Docker Desktop extension that runs the latest stable LiteLLM proxy and provides quick access to its UI and API docs." \
      com.docker.extension.publisher-url="https://github.com/berriai/litellm" \
      com.docker.extension.changelog=""

COPY metadata.json .
COPY icon.svg .
COPY compose.yaml .
COPY ui ./ui
