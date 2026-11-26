#!/usr/bin/env bash
# 编译 Linux 二进制，供 Ubuntu / 宝塔部署。
# 用法（在仓库根目录）:
#   bash scripts/build_linux.sh          # 默认 amd64，并覆盖 ./petlink-gateway
#   bash scripts/build_linux.sh arm64
# 只要产物、不覆盖根目录：INSTALL=0 bash scripts/build_linux.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

ARCH="${1:-amd64}"   # amd64 | arm64
OUT_DIR="${OUT_DIR:-dist/linux-${ARCH}}"
NAME="${NAME:-petlink-gateway}"
OUT_FILE="${OUT_DIR}/${NAME}"
INSTALL="${INSTALL:-1}"

if ! command -v go >/dev/null 2>&1; then
  echo "未找到 go，请先安装 Go 1.22+" >&2
  exit 1
fi

# 强制使用国内模块代理（避免 proxy.golang.org 超时）
export GOPROXY=https://goproxy.cn,direct
export GOSUMDB=sum.golang.google.cn

mkdir -p "$OUT_DIR"

export GOOS=linux
export GOARCH="$ARCH"
export CGO_ENABLED=0

echo "GOPROXY=$GOPROXY"
echo "编译中: GOOS=$GOOS GOARCH=$GOARCH CGO_ENABLED=0 -> $OUT_FILE"
go mod download
go build -trimpath -ldflags="-s -w" -o "$OUT_FILE" ./cmd
chmod +x "$OUT_FILE"

SIZE=$(wc -c < "$OUT_FILE" | tr -d ' ')
echo "完成: $OUT_FILE ($(( SIZE / 1024 / 1024 )) MB)"

# 校验：旧二进制含「目前仅支持 sqlite」；新版应为「仅支持 sqlite / mysql」
if command -v strings >/dev/null 2>&1; then
  if strings "$OUT_FILE" | grep -Fq '目前仅支持 sqlite'; then
    echo "警告: 产物仍是旧逻辑（database 只认 sqlite）。请先 git pull 最新代码再编译。" >&2
    exit 1
  fi
  if strings "$OUT_FILE" | grep -Fq '仅支持 sqlite / mysql'; then
    echo "校验通过: 产物已支持 mysql"
  else
    echo "警告: 未在产物中找到 mysql 校验文案，请确认源码已更新。" >&2
  fi
fi

if [[ "$INSTALL" == "1" || "$INSTALL" == "true" ]]; then
  DEST="$ROOT/$NAME"
  cp -f "$OUT_FILE" "$DEST"
  chmod +x "$DEST"
  echo "已覆盖运行文件: $DEST"
fi

echo ""
echo "下一步:"
echo "  1) 确认配置: grep -A3 '^database:' config/config.yaml   # driver 应为 mysql"
echo "  2) 宝塔「项目执行文件」必须指向刚覆盖的: $ROOT/$NAME"
echo "     （不要仍指向旧路径或未更新的二进制）"
echo "  3) 停掉旧进程后再启动，启动日志应出现: 音频入库已启用 driver=mysql"
echo "  手动试跑: $ROOT/$NAME -config $ROOT/config/config.yaml"
