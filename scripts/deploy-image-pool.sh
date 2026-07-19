#!/usr/bin/env bash
set -euo pipefail

# Deploy IMAGE POOL from the published GHCR image on a fresh Debian server.
# Run as root. Optional overrides:
#   IMAGE_POOL_PORT=8080 IMAGE_POOL_ADMIN_KEY=... bash deploy-image-pool.sh
#
# Keep IMAGE_POOL_IMAGE_TAG=latest for the built-in web updater. A fixed
# version tag can still be used for manual rollback/pinning, but Watchtower's
# HTTP API cannot advance a container from one fixed tag to another.

if [[ "$(id -u)" -ne 0 ]]; then
  echo "Run this script as root." >&2
  exit 1
fi

APP_DIR="${IMAGE_POOL_DIR:-/opt/image-pool}"
PORT="${IMAGE_POOL_PORT:-8080}"
IMAGE_TAG="${IMAGE_POOL_IMAGE_TAG:-latest}"
RAW_BASE="https://raw.githubusercontent.com/FlyLjx/IMAGEPOOL/main"
CONFIG_DIR="$APP_DIR/configs"
DATA_DIR="$APP_DIR/data"

install_docker() {
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    return
  fi

  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y ca-certificates curl python3
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc

  . /etc/os-release
  if [[ "${ID:-}" != "debian" || -z "${VERSION_CODENAME:-}" ]]; then
    echo "This script currently supports Debian servers." >&2
    exit 1
  fi
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian $VERSION_CODENAME stable" \
    >/etc/apt/sources.list.d/docker.list
  apt-get update
  apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  systemctl enable --now docker
}

install_docker

umask 077
install -d -m 0750 "$APP_DIR" "$CONFIG_DIR" "$DATA_DIR" "$DATA_DIR/images" "$APP_DIR/postgres-data"
curl -fsSL "$RAW_BASE/docker-compose.yml" -o "$APP_DIR/docker-compose.yml"

created_config=0
admin_key="${IMAGE_POOL_ADMIN_KEY:-}"
if [[ ! -f "$CONFIG_DIR/config.json" ]]; then
  if [[ -z "$admin_key" ]]; then
    admin_key="ip_$(python3 -c 'import secrets; print(secrets.token_urlsafe(32))')"
  fi
  curl -fsSL "$RAW_BASE/configs/config.example.json" -o "$CONFIG_DIR/config.example.json"
  python3 - "$CONFIG_DIR/config.example.json" "$CONFIG_DIR/config.json" "$admin_key" <<'PY'
import json
import pathlib
import sys

source = pathlib.Path(sys.argv[1])
target = pathlib.Path(sys.argv[2])
key = sys.argv[3]
config = json.loads(source.read_text(encoding="utf-8"))
config["api_keys"] = [key]
config["storage_backend"] = "postgres"
config["database_url"] = "postgresql://imagepool:imagepool@postgres:5432/imagepool?sslmode=disable"
config["listen_addr"] = ":8080"
target.write_text(json.dumps(config, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
PY
  chmod 600 "$CONFIG_DIR/config.json"
  created_config=1
fi

update_token="$(python3 -c 'import secrets; print(secrets.token_urlsafe(32))')"
cat >"$APP_DIR/.env" <<EOF
COMPOSE_PROJECT_NAME=image-pool
IMAGE_POOL_IMAGE=ghcr.io/flyljx/imagepool
IMAGE_POOL_IMAGE_TAG=$IMAGE_TAG
IMAGE_POOL_PORT=$PORT
IMAGE_POOL_UPDATE_TOKEN=$update_token
EOF
chmod 600 "$APP_DIR/.env"

if command -v ufw >/dev/null 2>&1 && ufw status | grep -q '^Status: active'; then
  ufw allow "$PORT/tcp"
fi

cd "$APP_DIR"
docker compose pull
docker compose up -d --remove-orphans

for _ in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:$PORT/health" >/dev/null; then
    echo "IMAGE POOL is ready at http://SERVER_IP:$PORT"
    if [[ "$created_config" -eq 1 ]]; then
      echo "Initial administrator API key: $admin_key"
    else
      echo "Existing config.json was retained."
    fi
    exit 0
  fi
  sleep 2
done

docker compose logs --tail=120 --no-color image-pool
exit 1
