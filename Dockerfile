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
RUN go mod download
COPY . ./
RUN go test ./... && go build -trimpath -ldflags="-s -w" -o /out/image-pool ./cmd/image-pool

FROM alpine:3.20
WORKDIR /app

RUN apk add --no-cache ca-certificates nodejs tzdata
ENV TZ=Asia/Shanghai

COPY --from=go-build /out/image-pool /app/image-pool
COPY --from=web-build /web/out /app/web_dist
COPY configs/config.example.json /app/default-config.json
COPY cmd/docker-entrypoint.sh /app/docker-entrypoint.sh
RUN chmod +x /app/docker-entrypoint.sh

EXPOSE 8080
ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["/app/image-pool", "-config", "/app/configs/config.json"]
