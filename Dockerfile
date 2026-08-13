FROM oven/bun:1.3.9-alpine AS web-build
WORKDIR /web

COPY VERSION CHANGELOG.md /
COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile
COPY web/ ./
RUN bun run build

FROM golang:1.24-alpine AS go-build
WORKDIR /src

COPY go.mod go.sum ./
# Public module mirrors can occasionally time out in GitHub Actions. Retry the
# download and allow a direct VCS fallback before failing the image build.
RUN for attempt in 1 2 3 4; do \
      GOPROXY="https://proxy.golang.org|direct" go mod download && exit 0; \
      echo "go mod download failed (attempt ${attempt}/4), retrying..." >&2; \
      sleep $((attempt * 3)); \
    done; \
    exit 1
COPY . ./
RUN go test ./... && go build -trimpath -ldflags="-s -w" -o /out/image-pool ./cmd/image-pool

FROM node:22-bookworm-slim
WORKDIR /app

COPY --from=go-build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
ENV TZ=Asia/Shanghai

COPY --from=go-build /out/image-pool /app/image-pool
COPY --from=web-build /web/out /app/web_dist
COPY configs/config.example.json /app/default-config.json
COPY cmd/docker-entrypoint.sh /app/docker-entrypoint.sh
RUN chmod +x /app/docker-entrypoint.sh

EXPOSE 8080
ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["/app/image-pool", "-config", "/app/configs/config.json"]
