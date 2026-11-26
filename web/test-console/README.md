# PetLink 网关前端测试台

本地浏览器联调工具：激活设备、连 WSS、发定位/音频双帧、下发命令与 TTS。

## 配置

| 变量 | 含义 |
|------|------|
| `VITE_DEV_PORT` | 测试台开发端口（仅 `npm run dev`） |
| `VITE_GATEWAY_ORIGIN` | **完整**网关 base URL（含协议；端口仅在你需要时自己写上） |

`VITE_GATEWAY_ORIGIN` **配置后**，开发与生产都会直连该地址（HTTP + WS），不再用本机 `location.host`。

```env
VITE_GATEWAY_ORIGIN=https://api.example.com
```

- 开发：复制为 `.env.local`（会覆盖 `.env.development`），再 `npm run dev`  
- 生产：写入 `.env.production` 后 `npm run build`  

未配置时才走相对路径 / 当前页 host（代理模式）。

activate 返回的 `server_url` 来自**网关** `public_base_url`；测试台展示与连 WS 优先用 `.env`。若两边不一致会在日志里提示。

## 本地开发

```powershell
cd web\test-console
npm install
npm run dev
```

开发时浏览器走相对路径，由 Vite 把 `/api`、`/ws` 代理到 `VITE_GATEWAY_ORIGIN`。

## 生产部署

```powershell
copy .env.production.example .env.production
# 填入完整 base URL（不要末尾 /）
npm run build
```

上传 `dist/`。若未配置 `VITE_GATEWAY_ORIGIN`，请求会打到前端站点自身，Nginx 静态站对 POST 常返回 **405**。

可选：在前端站点 Nginx 反代 `/api`、`/healthz`、`/ws` 到网关后，可将生产 `VITE_GATEWAY_ORIGIN` 留空，改用相对路径。

## 协议文档页

顶部切换 **「协议文档」**。页签按分工排列，点正文里的文档链接会切到对应篇：

- `/#docs-device-status` — 设备状态与配置（姿态枚举、ping/status，准绳）
- `/#docs` — 嵌入式接口（握手、音频、命令、烧录）
- `/#docs-cmd` — 三方对表（命令 / 字段 / 错误码）
- `/#docs-app-audio` — App 接口（音频、状态、下发）
- `/#docs-play-cry` — 固定叫声对照表
- `/#docs-debug` — 联调手册（只写步骤）

页内可再切换文档；内容直接引用仓库 `docs/*.md`：

- `npm run dev`：改 md 后前端会跟着更新  
- `npm run build`：打包进 `dist`，下载链接同步到 `public/docs/`

当前定稿要点：`charge`=`true`/`false`；`set_perf_mode`=`0`/`1`；`set_loc_interval` 毫秒；时间戳方案 A（Unix 微秒）；BLE 充电 `CHARGE:<电量%>`；`set_bitrate`=`"0"`/`"1"`/`"2"`（只影响实时翻译 `type=audio` OPUS 上行；`record_now` 固定 32kbps）；`tier`=`normal`/`light`/`deep`（功耗档位）。HTTP 业务成功一律 **`code:1`**（失败 `code:0`）。

## 推荐操作顺序

1. Activate → 2. 连接 WS → 3. 发定位/音频 → 4. 下发命令/TTS
