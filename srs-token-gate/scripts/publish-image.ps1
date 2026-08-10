$ErrorActionPreference = 'Stop'

$projectRoot = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$tokenAuth = Join-Path $projectRoot 'srs-token-gate/token-auth'
$image = if ($env:TOKEN_GATE_IMAGE) { $env:TOKEN_GATE_IMAGE } else { 'docker.io/guanren/srs-token-gate:latest' }
$proxy = if ($env:GOPROXY) { $env:GOPROXY } else { 'https://goproxy.cn,direct' }

Write-Host 'Running Go tests...'
Push-Location $tokenAuth
try {
    go test ./...
    go test -race ./...
    go vet ./...
} finally {
    Pop-Location
}

node --check (Join-Path $projectRoot 'srs-token-gate/conf/console/js/srs.token.js')
docker info --format '{{.ServerVersion}}' | Out-Null
docker buildx version | Out-Null

Write-Host "Building and pushing $image for linux/amd64..."
docker buildx build `
    --platform linux/amd64 `
    --tag $image `
    --build-arg "GOPROXY=$proxy" `
    --push `
    $tokenAuth

$digest = docker buildx imagetools inspect $image --format '{{.Manifest.Digest}}'
Write-Host "Published image digest: $digest"
