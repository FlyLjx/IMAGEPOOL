param(
  [switch]$BuildFrontend
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

docker compose up -d postgres

$env:DATABASE_URL = "postgresql://imagepool:imagepool@127.0.0.1:5434/imagepool?sslmode=disable"
$env:IMAGE_POOL_LISTEN_ADDR = ":18081"
$env:IMAGE_POOL_WEB_DIST_DIR = Join-Path $root "web\out"
$env:IMAGE_POOL_IMAGE_OUTPUT_DIR = Join-Path $root "data\images"
$env:IMAGE_POOL_UPSTREAM_TRANSPORT = "tls_client"

if ($BuildFrontend -or -not (Test-Path (Join-Path $env:IMAGE_POOL_WEB_DIST_DIR "index.html"))) {
  Push-Location (Join-Path $root "web")
  try {
    bun run build
  } finally {
    Pop-Location
  }
}

New-Item -ItemType Directory -Force -Path (Join-Path $root ".tmp") | Out-Null
go build -o (Join-Path $root ".tmp\image-pool-local.exe") ./cmd/image-pool
& (Join-Path $root ".tmp\image-pool-local.exe") -config ./configs/config.json
