FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /config-server .

FROM scratch
COPY --from=build /config-server /config-server
EXPOSE 8080
HEALTHCHECK --interval=2s --timeout=2s --retries=5 \
  CMD ["/config-server", "-health-check"]
ENTRYPOINT ["/config-server"]
