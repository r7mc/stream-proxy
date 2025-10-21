# ========= Build stage =========
FROM golang:tip-trixie AS build
WORKDIR /src
# RUN apk add --no-cache git ca-certificates  # 仅在有第三方依赖时需要

COPY . .
ENV CGO_ENABLED=0

RUN mkdir -p /out
RUN go build -v -trimpath -ldflags "-s -w -buildid=" -o /out/stream-proxy .

# ========= Run stage =========
FROM alpine:latest
WORKDIR /app

# 安装必要的 ca-certificates（用于 HTTPS 请求）
RUN apk add --no-cache ca-certificates

# 拷贝二进制
COPY --from=build /out/stream-proxy /app/stream-proxy

# 环境变量
ENV STREAM_CONFIG=/app/config.json

EXPOSE 8000
ENTRYPOINT ["/app/stream-proxy"]


