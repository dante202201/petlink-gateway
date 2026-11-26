# 生成本地自签 TLS 证书（仅用于开发联调）
# 用法: .\scripts\gen_certs.ps1
# 可选: .\scripts\gen_certs.ps1 -Days 365

param(
    [int]$Days = 3650
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
if (-not $Root) { $Root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path }
$CertDir = Join-Path $Root "certs"

if (-not (Get-Command openssl -ErrorAction SilentlyContinue)) {
    Write-Error "未找到 openssl，请先安装 OpenSSL 并加入 PATH"
}

New-Item -ItemType Directory -Force -Path $CertDir | Out-Null

$key = Join-Path $CertDir "server.key"
$crt = Join-Path $CertDir "server.crt"

openssl req -x509 -newkey rsa:2048 -nodes `
  -keyout $key `
  -out $crt `
  -days $Days `
  -subj "/CN=localhost" `
  -addext "subjectAltName=DNS:localhost,DNS:*.localhost,IP:127.0.0.1,IP:::1"

if ($LASTEXITCODE -ne 0) {
    Write-Error "openssl 生成失败，exit=$LASTEXITCODE"
}

Write-Host "证书已生成（${Days} 天有效）:"
Write-Host "  $crt"
Write-Host "  $key"
Write-Host "对应 config.yaml: server.cert_file / server.key_file"
Write-Host "联调客户端请跳过证书校验（自签）。"
