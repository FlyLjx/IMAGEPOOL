#!/bin/sh
set -eu

mkdir -p /app/configs /app/data
if [ ! -s /app/configs/config.json ]; then
  cp /app/default-config.json /app/configs/config.json
fi

exec "$@"
