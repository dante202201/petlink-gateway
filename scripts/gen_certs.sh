#!/usr/bin/env bash
# 生成本地自签 TLS 证书（仅用于开发联调）
# Linux / macOS / Git Bash: bash scripts/gen_certs.sh
# Windows PowerShell:      .\scripts\gen_certs.ps1

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CERT_DIR="$ROOT/certs"
DAYS="${CERT_DAYS:-3650}"

if ! command -v openssl >/dev/null 2>&1; then
  echo "错误: 未找到 openssl，请先安装 OpenSSL 并加入 PATH" >&2
  exit 1
fi

mkdir -p "$CERT_DIR"

# 覆盖生成；含 SAN，浏览器/客户端校验证书时可用 localhost / 127.0.0.1
openssl req -x509 -newkey rsa:2048 -nodes \
  -keyout "$CERT_DIR/server.key" \
  -out "$CERT_DIR/server.crt" \
  -days "$DAYS" \
  -subj "/CN=localhost" \
  -addext "subjectAltName=DNS:localhost,DNS:*.localhost,IP:127.0.0.1,IP:::1"

echo "证书已生成（${DAYS} 天有效）:"
echo "  $CERT_DIR/server.crt"
echo "  $CERT_DIR/server.key"
echo "对应 config.yaml: server.cert_file / server.key_file"
echo "联调客户端请跳过证书校验（自签）。"
