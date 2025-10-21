# ========= Build stage =========
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY . .
ENV CGO_ENABLED=0
RUN --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags "-s -w -buildid=" -o /out/stream-proxy .

# ========= Run stage =========
FROM gcr.io/distroless/static:nonroot
WORKDIR /app
COPY --from=build /out/stream-proxy /app/stream-proxy
ENV STREAM_CONFIG=/app/config.json
EXPOSE 8000
USER nonroot:nonroot
ENTRYPOINT ["/app/stream-proxy"]
