# 交叉编译 Linux amd64 二进制，供 Ubuntu / 宝塔部署。
# 用法（在仓库根目录）:
#   .\scripts\build_linux.ps1
#   .\scripts\build_linux.ps1 -OutDir .\dist -Name petlink-gateway
param(
    [string]$OutDir = ".\dist\linux-amd64",
    [string]$Name = "petlink-gateway",
    [string]$Arch = "amd64"  # amd64 | arm64
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
Set-Location $Root

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Error "未找到 go，请先安装 Go 1.22+"
}

# 强制使用国内模块代理（避免 proxy.golang.org 超时）
$env:GOPROXY = "https://goproxy.cn,direct"
$env:GOSUMDB = "sum.golang.google.cn"

New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$OutFile = Join-Path $OutDir $Name

$prevGOOS = $env:GOOS
$prevGOARCH = $env:GOARCH
$prevCGO = $env:CGO_ENABLED
try {
    $env:GOOS = "linux"
    $env:GOARCH = $Arch
    $env:CGO_ENABLED = "0"

    Write-Host "GOPROXY=$env:GOPROXY"
    Write-Host "编译中: GOOS=linux GOARCH=$Arch CGO_ENABLED=0 -> $OutFile"
    go mod download
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    go build -trimpath -ldflags="-s -w" -o $OutFile .\cmd
    if ($LASTEXITCODE -ne 0) {
        exit $LASTEXITCODE
    }
}
finally {
    if ($null -eq $prevGOOS) { Remove-Item Env:GOOS -ErrorAction SilentlyContinue } else { $env:GOOS = $prevGOOS }
    if ($null -eq $prevGOARCH) { Remove-Item Env:GOARCH -ErrorAction SilentlyContinue } else { $env:GOARCH = $prevGOARCH }
    if ($null -eq $prevCGO) { Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue } else { $env:CGO_ENABLED = $prevCGO }
}

$info = Get-Item $OutFile
Write-Host ("完成: {0} ({1:N1} MB)" -f $info.FullName, ($info.Length / 1MB))
Write-Host "上传到 Ubuntu 后: chmod +x $Name && ./$Name -config ./config/config.yaml"
