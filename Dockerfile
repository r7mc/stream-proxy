# ========= Build stage =========
FROM golang:1.22-alpine AS build
WORKDIR /src
# RUN apk add --no-cache git ca-certificates  # 仅在有第三方依赖时需要

COPY . .
ENV CGO_ENABLED=0

RUN mkdir -p /out
RUN go build -v -trimpath -ldflags "-s -w -buildid=" -o /out/stream-proxy .

# ========= Run stage =========
FROM gcr.io/distroless/static:nonroot
WORKDIR /app
COPY --from=build /out/stream-proxy /app/stream-proxy
ENV STREAM_CONFIG=/app/config.json
EXPOSE 8000
USER nonroot:nonroot
ENTRYPOINT ["/app/stream-proxy"]
