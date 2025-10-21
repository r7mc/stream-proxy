docker-compose.yml
```yaml
version: "3.8"
services:
  stream-proxy:
    image: ghcr.io/r7mc/stream-proxy:latest
    container_name: stream-proxy
    volumes:
      - ./config.json:/app/config.json:ro
    # 与 config.json 的 listen.port 保持一致
    ports:
      - "8000:8000"
    restart: unless-stopped

```
