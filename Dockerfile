FROM gcr.io/distroless/static:nonroot
WORKDIR /app
COPY build/stream-proxy /app/stream-proxy
ENV STREAM_CONFIG=/app/config.json
EXPOSE 8000
USER nonroot:nonroot
ENTRYPOINT ["/app/stream-proxy"]
