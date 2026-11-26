# PetLinkGateway — 宠物翻译硬件 WebSocket 网关

基于标准 Go 生态的物联网硬件专用 WSS 网关：接入拾音 OPUS 分片与 GPS/WiFi 定位上报，只做连接管理与消息转发，不写业务。

## 技术栈

| 组件 | 选型 |
|------|------|
| 语言 | Go 1.22+ |
| WebSocket | gorilla/websocket |
| HTTP | net/http + TLS(WSS) |
| 缓存 | Redis (go-redis) |
| 队列 | Kafka (IBM/sarama) |
| 日志 | zap |
| 配置 | viper + yaml |

## 目录结构

```
pet-translate-gateway/
├── config/                 # yaml 配置 + viper 加载
├── internal/
│   ├── gateway/            # WS 接入：握手/连接/心跳/解析/下发
│   ├── model/              # 上报与指令结构体
│   ├── redis/              # Redis 封装
│   ├── kafka/              # Kafka 生产者
│   └── util/               # 时间/CRC/证书热加载
├── pkg/logger/             # zap 封装
├── cmd/
│   ├── main.go             # 网关入口
│   └── demo_client/        # 本地模拟硬件客户端
├── scripts/gen_certs.sh    # 自签证书（bash）
├── scripts/gen_certs.ps1   # 自签证书（Windows）
├── go.mod
└── README.md
```

## 二进制混合帧协议

> **已按固件真实约定对齐（2026-08）**；完整说明见 [嵌入式接口使用文档](./docs/嵌入式接口使用文档.md)（测试台 `#docs` 在线同源）。

音频上报为 **双帧**：

1. Text 帧：JSON 元数据（`sn/type/codec/seq/total/end/pid/bat/charge/ts/crc/...`）
2. Binary 帧：**一个完整裸 Opus 包**（建议 **60ms**）；单独一条 BIN；`crc` 为该载荷 IEEE CRC32  
   - 禁止：多包拼一条 Binary、Ogg 容器、半包

要点：

- `charge`：`true`/`false`；独立充电 BLE STAT：`CHARGE:<0-100>`
- `ts`：**方案 A** Unix 微秒（段首绝对；seq>0 可为相对偏移微秒）；禁止裸 32 位毫秒
- 独立定位：`{"type":"locate"|geolocate, ...}`；HTTP 解析：`POST /api/device/locate`（别名 geolocate）
- 补发：可精简字段，云端回填 `codec/total/ts`
- 批量：先 `{"type":"batch","count":N}`，再 N 组 meta+bin

握手：`wss://host/ws?sn=设备SN&token=令牌`（亦兼容 Header）。

下行控制：`{"type":"cmd","cmd":"命令名","data":参数}`；TTS 同样是 Text 元数据 + **一条** Binary（一帧完整 Opus，建议 60ms）。

## Redis Key

统一前缀：`petlink:`（配置项 `redis.key_prefix`）

| Key | 说明 |
|-----|------|
| `petlink:pet:device:auth` (Hash) / `petlink:pet:device:token:{sn}` | 设备 Token（激活/刷新写入） |
| `petlink:pet:device:meta:{sn}` | 激活元信息（fw/mac） |
| `petlink:pet:online:{sn}` | 在线状态，TTL 略大于心跳 |
| `petlink:pet:conn:{sn}` | 连接映射缓存 |
| `petlink:pet:offline_cmd:{sn}` (List) | 离线下行指令 |
| `petlink:pet:wifi_fp:{sha1}` | WiFi MAC 指纹 → 坐标 |
| `petlink:pet:device:last_loc:{sn}` | 设备最近一次定位解析结果 |
| `petlink:baidu_translate_access_token` | 百度 TTS access_token 缓存 |

## HTTP 设备接口（固件约定）

### 1. 激活 `POST /api/device/activate`

```json
// 请求
{"sn":"TEST_SN_001","fw_ver":"1.0.0","mac":"aa:bb:cc:dd:ee:ff"}
// 响应 200
{"code":0,"data":{"token":"<hex>","server_url":"wss://127.0.0.1:8443/ws"}}
```

### 2. Token 刷新 `POST /api/device/refresh`

```json
// 请求（旧 token）
{"sn":"TEST_SN_001","token":"<old>"}
// 响应 200
{"code":0,"data":{"token":"<new>"}}
```

### 3. WiFi 定位解析 `POST /api/device/locate`（别名 `/api/device/geolocate`）

```json
// 请求
{"sn":"TEST_SN_001","wifi_scan":[{"mac":"aa:bb:cc:01","rssi":-40},{"mac":"aa:bb:cc:02","rssi":-55}]}
// 响应 200
{"code":0,"data":{"lat":31.2304,"lng":121.4737,"accuracy":50}}
```

也兼容 `wifiAccessPoints`（`macAddress`/`signalStrength`）等写法。

联调校准指纹（可选）：`POST /api/v1/locate/bind` 把某组 MAC 绑到真实经纬度。

### 4. 固件最新版本 `POST /api/device/firmware`（需 token）

```json
// 响应
{"code":1,"data":{"version":"1.2.0","url":"https://cdn/.../x.bin","force":false,"size":1048576,"md5":"...","accept_ranges":true}}
```

下载：`GET /api/device/firmware/download`（Header `Range: bytes=N-` 续传，鉴权同 firmware）。

## 快速开始

> **嵌入式对接文档：** [docs/嵌入式接口使用文档.md](./docs/嵌入式接口使用文档.md)
> **三方对表（命令/上报）：** [docs/三方对表-命令与上报.md](./docs/三方对表-命令与上报.md)
> **App 音频查询：** [docs/App音频查询接口.md](./docs/App音频查询接口.md)
> **App ↔ 设备 BLE：** [docs/App-BLE通道协议.md](./docs/App-BLE通道协议.md)（分包/MTU，非网关）
> **翻译流水线（OSS→AI→百度TTS）：** [docs/音频翻译流水线.md](./docs/音频翻译流水线.md)
> **ApiPost 逐步联调请看：** [docs/联调手册-ApiPost.md](./docs/联调手册-ApiPost.md)
> **浏览器测试台：** [web/test-console](./web/test-console)（`npm run dev` → http://127.0.0.1:5173）

### 1. 依赖环境

- Go 1.22+
- Redis
- Kafka
- OpenSSL（生成开发证书）

### 2. 生成 TLS 证书

**Linux / macOS / Git Bash：**

```bash
bash scripts/gen_certs.sh
```

**Windows PowerShell：**

```powershell
.\scripts\gen_certs.ps1
```

可选有效天数：`.\scripts\gen_certs.ps1 -Days 365`（bash 用环境变量 `CERT_DAYS=365`）。

生成文件：`certs/server.crt`、`certs/server.key`（含 localhost / 127.0.0.1 SAN）。路径对应 `config/config.yaml` 中 `server.cert_file` / `server.key_file`。生产请换成正式证书；运行中替换文件后按 `cert_reload_interval` 热加载。

### 3. 激活设备（推荐，替代手工写 Redis）

```bash
curl -k -X POST https://127.0.0.1:8443/api/device/activate \
  -H "Content-Type: application/json" \
  -d "{\"sn\":\"TEST_SN_001\",\"fw_ver\":\"1.0.0\",\"mac\":\"aa:bb:cc:dd:ee:ff\"}"
```

或仍可手工写入：

```bash
# Hash 方式（推荐）
redis-cli -n 10 HSET petlink:pet:device:auth TEST_SN_001 test-token-001
```

### 4. Kafka（可选）

本地联调默认 `kafka.enabled: false`，**可不装 Kafka**，音频/定位只打日志。

生产打开：

```yaml
kafka:
  enabled: true
```

并创建 topic：

```bash
kafka-topics.sh --create --topic topic_pet_audio --bootstrap-server 127.0.0.1:9092 --partitions 3 --replication-factor 1
kafka-topics.sh --create --topic topic_pet_location --bootstrap-server 127.0.0.1:9092 --partitions 3 --replication-factor 1
```

### 5. 修改配置

编辑 `config/config.yaml`：监听地址、Redis、Kafka、心跳与限流等，**禁止硬编码**。

### 6. 启动网关

```bash
go mod tidy
go run ./cmd -config ./config/config.yaml
```

默认监听 `wss://0.0.0.0:8443/ws`。

### 7. 本地模拟硬件联调

```bash
go run ./cmd/demo_client `
  -host wss://127.0.0.1:8443/ws `
  -sn TEST_SN_001 `
  -token test-token-001 `
  -insecure
```

Demo 会依次：

1. 带 Header 鉴权握手
2. 发送纯 JSON 定位包
3. 发送 3 个 OPUS 混合分片（含定位复用字段）
4. 发送断网补发重复分片（验证去重）
5. 保持连接接收下行指令

### 8. 测试下行指令

设备在线时：

```bash
curl -k -X POST https://127.0.0.1:8443/api/v1/device/cmd `
  -H "Content-Type: application/json" `
  -d "{\"sn\":\"TEST_SN_001\",\"cmd\":\"reboot\",\"data\":{}}"
```

支持的下行命令（外层 `{"type":"cmd","cmd":"...","data":...}`，详见 `docs/嵌入式接口使用文档.md`）：

- `set_sensitivity` / `set_volume` / `enable_auto_record`（`"true"`/`"false"`，自动录音）/ `enable_translate`（旧名 `0`/`1`） / `enable_sleep`
- `set_sleep_window`（**`"0:6"`** 开始:结束小时）/ `set_timezone`（UTC 偏移分钟，北京 `480`）
- `set_perf_mode`（`0`=low / `1`=high）/ `set_pet_type` / `set_bitrate`（`"0"`/`"1"`/`"2"`；**只影响**实时翻译 `type=audio` OPUS 上行；`record_now` 固定 32kbps）
- `enable_location` / `set_loc_interval`（**毫秒**，如 `30000`）
- `set_home_wifi`（SSID 字符串）
- `reboot` / `factory_reset` / `ota_update`（`{url,version,force:true/false,size,md5,accept_ranges}`）
- `bind_device`（`{pid,breed}`）/ `unbind_device`
- `set_record_mode`（`0`=连续每帧上传 / `1`=事件触发有声才传，默认；非开关） / `set_upload_mode`（`0`/`1`）
- `play_cry`（`data:"0x65"` 叫声播放）
- `token_refresh`；令牌过期通知为 `{"type":"notify","event":"token_expired"}`
- 围栏 `set_fence`（`{lat,lng,radius}` 单圆）/ `clear_fence`
- 三方对表：[docs/三方对表-命令与上报.md](./docs/三方对表-命令与上报.md)

```go
cmd := gateway.BuildCommand(model.CmdReboot, map[string]interface{}{})
```

设备离线时指令写入 `pet:offline_cmd:{sn}`，重连后自动批量下发并清空。

健康检查：

```bash
curl -k https://127.0.0.1:8443/healthz
```

## 硬件对接要点

1. **握手**：`WSS` Upgrade，Header 必须带 `X-Device-SN`、`X-Device-Token`；失败返回 403
2. **心跳**：设备每 25s 发 Ping；服务端也会 Ping，8s 无 Pong/活动则断开并标记离线
3. **限流**：单设备默认 20 msg/s，超限主动断开
4. **重入**：同 SN 新连接自动踢掉旧连接
5. **边界**：网关不拼接音频、不解码 OPUS、不做 AI、不入库、不做电子围栏；只透传 Kafka

## 代码侧调用下行

```go
cmd := gateway.BuildCommand(model.CmdReboot, "")
err := mgr.SendCmdToDevice(sn, cmd)
// TTS（单帧 60ms OPUS）
err = mgr.SendTTSToDevice(sn, opusBytes)
```

## 优雅关闭

捕获 `SIGINT` / `SIGTERM`：主动断开全部连接 → 关闭 HTTP → 释放 Redis/Kafka。

## 生产部署建议

1. 监听 `:443` 或 `:8443`，使用正式证书，关闭客户端 `InsecureSkipVerify`
2. 将 Redis / Kafka 换成集群地址，调整 topic 分区
3. 日志级别 `info`，配置文件挂载只读，证书目录可写以支持热更新
4. 使用 systemd / 宝塔 Go 项目 / Supervisor 托管进程，配置异常自动重启

---

## 宝塔面板部署（Go 项目）

两种模式二选一：

| 模式 | 适用 | 对外地址 |
|------|------|----------|
| **A. 反代（推荐，443 无端口）** | 网站已绑域名+SSL，反代到 Go | `wss://域名/ws` |
| **B. Go 自带 TLS** | 不反代，直接放行 8443 | `wss://域名:8443/ws` |

你现在用的是 A，但 Go 若仍开 TLS，WebSocket 升级容易 1006。请用下面 **模式 A**。

### 模式 A：Nginx 终结 TLS + Go 明文（解决测试台 1006）

**1）`config/config.yaml`：**

```yaml
server:
  addr: "127.0.0.1:8088"   # 只本机，与宝塔反代目标一致
  behind_proxy: true         # 明文 HTTP，不读证书
  ws_path: "/ws"
  public_base_url: "wss://api.example.com"  # 不要带端口
```

宝塔 Go 项目「项目端口」填 `8088`，执行文件照旧，改完 yaml 后**重启 Go 项目**。

**2）网站 → 反向代理**：目标填 `http://127.0.0.1:8088`（必须是 **http**，不是 https）。

**3）网站配置 → 自定义配置**（server 段）增加，保存并重载 Nginx：

```nginx
location ^~ /ws {
    proxy_pass http://127.0.0.1:8088;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_connect_timeout 10s;
    # 音频是小包高频长连接，超时过短会被掐且没有 Close 帧
    proxy_read_timeout 3600s;
    proxy_send_timeout 3600s;
    proxy_buffering off;
    proxy_request_buffering off;
    gzip off;
}

# 固件下载：必须把文件流透传，禁止 return 302 / rewrite 到 OSS
location ^~ /api/device/firmware/download {
    proxy_pass http://127.0.0.1:8088;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_buffering off;
    proxy_request_buffering off;
    proxy_max_temp_file_size 0;
    proxy_read_timeout 300s;
    proxy_send_timeout 300s;
    gzip off;
    # 不要 limit_rate / limit_req
}
```

**4）本机自检：**

```bash
curl -s http://127.0.0.1:8088/healthz
# 连测试台 WS 时，网关日志应出现「握手开始」「设备上线」
```

### 模式 B：Go 自带 TLS（不反代）

本网关 `behind_proxy: false` 时自行 `ListenAndServeTLS`。防火墙放行 **8443**，`public_base_url` 带端口：`wss://域名:8443`。**不要**再对 8443 套 Nginx 反代。

### 1. 依赖

软件商店安装：

| 组件 | 说明 |
|------|------|
| Redis | 必装（token / 在线状态） |
| ffmpeg | 仅当 `translate.enabled: true` 时需要（含 libopus） |

Kafka 可保持 `kafka.enabled: false`。

### 2. 目录与编译

建议部署目录（示例，按你服务器实际路径改）：

```text
/www/wwwroot/PetLinkGateway/
├── petlink-gateway          # 编译后的二进制（项目执行文件）
├── config/config.yaml
├── certs/                   # 模式 B 需要；模式 A 可省略
├── data/                    # SQLite（database.driver=sqlite 时）
└── logs/
```

编译（在服务器有 Go 时）：

```bash
cd /www/wwwroot/PetLinkGateway
bash scripts/build_linux.sh          # 默认 linux/amd64 → dist/linux-amd64/petlink-gateway
# bash scripts/build_linux.sh arm64  # ARM 机器
```

脚本内已强制 `GOPROXY=https://goproxy.cn,direct`，一般无需再手动 `go env -w`。

本机（Windows）交叉编译再上传：

```powershell
# 在仓库根目录
.\scripts\build_linux.ps1
# 产物: dist\linux-amd64\petlink-gateway
```

```bash
# Git Bash / macOS / Linux
bash scripts/build_linux.sh
```

把 `dist/linux-amd64/petlink-gateway` 拷到项目根（或宝塔执行文件指向该路径），并确保有 `config/config.yaml`。

### 3. 配置文件要点（模式 B 示例）

```yaml
server:
  addr: ":8443"
  behind_proxy: false
  cert_file: "/www/server/panel/vhost/cert/你的域名/fullchain.pem"
  key_file: "/www/server/panel/vhost/cert/你的域名/privkey.pem"
  ws_path: "/ws"
  public_base_url: "wss://你的域名:8443"

redis:
  addr: "127.0.0.1:6379"
  password: "宝塔里设置的Redis密码"
  db: 10
```

### 4. 添加 Go 项目时怎么填

宝塔 → 网站 → **Go 项目** → 添加：

| 表单项 | 填写示例 | 说明 |
|--------|----------|------|
| 项目名称 | `PetLinkGateway` | 面板显示名，随意 |
| 项目执行文件 | `/www/wwwroot/PetLinkGateway/petlink-gateway` | **编译后的二进制**，不要选 `main.go` |
| 项目端口 | `8088`（模式 A）或 `8443`（模式 B） | 必须与 `server.addr` 一致 |
| 执行命令 | 见下方 | 务必带 `-config` |
| 运行目录（若有） | `/www/wwwroot/PetLinkGateway` | 相对路径证书/日志/库才正确 |

**执行命令（推荐绝对路径）：**

```bash
/www/wwwroot/PetLinkGateway/petlink-gateway -config /www/wwwroot/PetLinkGateway/config/config.yaml
```

### 5. 验证

模式 A：

```bash
curl -s http://127.0.0.1:8088/healthz
curl -s -X POST https://你的域名/api/device/activate \
  -H "Content-Type: application/json" \
  -d '{"sn":"TEST_SN_001","fw_ver":"1.0.0","mac":"aa:bb:cc:dd:ee:ff"}'
```

模式 B：

```bash
curl -k https://127.0.0.1:8443/healthz
```

### 6. 注意

- 模式 A：外网只走 443；Go 建议绑 `127.0.0.1`，且反代必须 `http://`。
- 模式 B：不要再对 Go 端口做反代。
- 固件 OTA 只改 yaml 的 `firmware` 段，约 10s 热加载，不必重启进程。
- 更新二进制后：重新编译覆盖执行文件 → 宝塔 Go 项目里重启。