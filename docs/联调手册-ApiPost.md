# PetLinkGateway 本地联调手册（ApiPost）

> 目标：用 **ApiPost** 尽量测完当前已实现环节。  
> 适用版本：协议对齐后的网关（握手 query、双帧音频、HTTP 三件套、Kafka 可关、**设备状态查询 H8/H9/H10**）。  
> 整理时间：2026-08-03（更新 2026-09-21：只保留操作步骤；字段含义以嵌入式 / 设备状态 / App 音频三篇为准，不在本手册再抄协议）  
> 在线查阅：测试台 **协议文档 → 联调手册**（`/#docs-debug`）

---

## 1. 结论先看：ApiPost 能测多少？

| 环节 | ApiPost 能否覆盖 | 说明 |
|------|------------------|------|
| 健康检查 `/healthz` | ✅ 完全可以 | HTTP GET |
| 设备激活 activate | ✅ 完全可以 | HTTP POST |
| Token 刷新 refresh | ✅ 完全可以 | HTTP POST |
| WiFi 定位解析 locate | ✅ 完全可以 | HTTP POST |
| 指纹校准 bind | ✅ 完全可以 | HTTP POST |
| 下行控制命令 cmd | ✅ 可测（需设备/模拟端在线） | HTTP 触发，WS 侧收包 |
| TTS 下发 | ✅ 可测（需在线） | HTTP 发，WS 收 Binary |
| **单台设备状态查询** | ✅ 完全可以 | H8 `/api/v1/device/status?sn=` |
| **批量设备状态列表** | ✅ 完全可以 | H9 `/api/v1/device/status/list` |
| **BLE STAT → 状态快照联动** | ✅ 完全可以 | H10：WS 发 `CHARGE:78` + H8 验证字段 |
| WSS 握手鉴权 | ✅ 可以 | ApiPost WebSocket，URL 带 `?sn=&token=` |
| 独立定位 `type=loc` | ✅ 可以 | WS 发 Text |
| 批量头 `type=batch` | ✅ 可以 | WS 发 Text |
| 音频双帧 JSON+Binary | ⚠️ 看版本 | 需支持「先 Text 再 Binary」连续发送；Binary 要能发原始字节 |
| CRC 校验 | ⚠️ 半自动 | JSON 里填预计算 crc；或先不管 crc（网关 crc=0 时跳过校验） |
| 补发去重 | ⚠️ 可测 | 同一 seq/total/ts 发两遍，看网关日志是否「重复分片跳过」 |
| 硬件 Ping/Pong 心跳 | ⚠️ 弱 | 多数客户端不方便按 25s 发 Ping 帧；可依赖服务端主动 Ping |
| 真机 OPUS/AES | ❌ 不够 | 需真机或 `demo_client` |

**一句话：**  
HTTP 全链路 + WSS 文本类消息，**ApiPost 够用**。  
也可用仓库内 **浏览器测试台** `web/test-console`（支持双帧音频）。  
音频双帧若 ApiPost 不支持 Binary，用测试台或 `demo_client`。

---

## 2. 启动前准备

### 2.1 依赖

| 组件 | 是否必须 | 备注 |
|------|----------|------|
| Redis | ✅ 必须 | 鉴权 / 在线 / 离线指令 |
| TLS 证书 | ✅ 必须 | `certs/server.crt` + `server.key` |
| Kafka | ❌ 可不装 | `config.yaml` 里 `kafka.enabled: false` |
| Go 网关进程 | ✅ 必须 | `go run ./cmd -config ./config/config.yaml` |

### 2.2 配置核对（`config/config.yaml`）

- `server.addr`: `:8443`
- `server.ws_path`: `/ws`
- `server.public_base_url`: `wss://127.0.0.1:8443`
- `redis.addr` / `password` / `db`：以你本机为准
- `kafka.enabled`: `false`（本地通路）

### 2.3 启动网关

```powershell
cd D:\work\go\PetLinkGateway
$env:Path = "C:\Program Files\Go\bin;" + $env:Path
go run ./cmd -config ./config/config.yaml
```

启动日志应出现类似：

- `kafka_enabled: false`
- `server_url: wss://127.0.0.1:8443/ws`

### 2.4 ApiPost 环境变量（建议建环境「本地网关」）

| 变量名 | 示例值 | 用途 |
|--------|--------|------|
| `base` | `https://127.0.0.1:8443` | HTTP 根地址 |
| `sn` | `TEST_SN_001` | 设备序列号 |
| `token` | （activate 后填入） | 鉴权令牌 |
| `ws` | `wss://127.0.0.1:8443/ws` | WebSocket 路径（不含 query） |

**SSL：** 本地自签证书，在 ApiPost 关闭证书校验 / 勾选「忽略 SSL」。

---

## 3. HTTP 接口清单（建议按顺序建用例）

> 统一 Header：`Content-Type: application/json`  
> Base URL：`{{base}}`

### 用例 H1 — 健康检查

- **方法/URL：** `GET {{base}}/healthz`
- **期望：** `200`
- **Body 示例：**

```json
{"status":"ok","online":0}
```

有设备连上后 `online` 会变大。

---

### 用例 H2 — 设备激活（最重要）

- **方法/URL：** `POST {{base}}/api/device/activate`
- **Body：**

```json
{
  "sn": "TEST_SN_001",
  "fw_ver": "1.0.0",
  "mac": "aa:bb:cc:dd:ee:ff"
}
```

- **期望：** HTTP `200`，信封 **`code: 1`**（业务成功；**不是** `code:0`）

```json
{
  "code": 1,
  "data": {
    "token": "一串hex",
    "server_url": "wss://api.example.com/ws"
  }
}
```

- **操作：** 把 `data.token` 写进环境变量 `{{token}}`；设备用 `data.server_url` 连 WSS（`?sn=&token=`）。
- **网关日志：** `设备激活成功`

> 也可用别名：`POST /api/v1/device/activate`（行为相同）

---

### 用例 H3 — Token 刷新

- **方法/URL：** `POST {{base}}/api/device/refresh`
- **Body：**

```json
{
  "sn": "{{sn}}",
  "token": "{{token}}"
}
```

- **期望：** `200`，返回新 `token`
- **操作：** 用新 token 覆盖 `{{token}}`
- **负例：** 故意改错旧 token → 期望 `403`

> 刷新后，旧 token 立刻失效；已建立的 WS 不会自动踢，但**新握手必须用新 token**。

---

### 用例 H4 — WiFi 定位解析

- **方法/URL：** `POST {{base}}/api/device/locate`
- **Body：**

```json
{
  "sn": "{{sn}}",
  "wifi_scan": [
    {"mac": "aa:bb:cc:dd:ee:01", "rssi": -40, "ssid": "Home"},
    {"mac": "aa:bb:cc:dd:ee:02", "rssi": -55}
  ]
}
```

- **期望：** `200`

```json
{
  "lat": 31.2304,
  "lng": 121.4737,
  "accuracy": 50
}
```

（数值来自 `locate.mock_*`；同组 MAC 再次请求会走 fingerprint 缓存）

- **负例：** 空 `wifi_scan` → `400`

---

### 用例 H5 — 固件最新版本

- **方法/URL：** `POST {{base}}/api/device/firmware`
- **Body：**

```json
{
  "sn": "{{sn}}",
  "token": "{{token}}",
  "fw_ver": "1.0.0"
}
```

- **也可用：** `GET {{base}}/api/device/firmware`（无需 sn / token）
- **前置：** activate 拿到 token；`config.yaml` 已填 `firmware.version` 与 `firmware.url`
- **期望：** `200`

```json
{
  "code": 1,
  "data": {
    "version": "1.0.0",
    "url": "https://...",
    "force": false,
    "size": 1048576,
    "md5": "...",
    "accept_ranges": true
  }
}
```

（`download_url` 可选；默认直链模式不返回。）

- **下载（断点续传）：** `GET {{base}}/api/device/firmware/download`  
  Header：`X-Device-SN` / `X-Device-Token`，续传加 `Range: bytes=已下载字节数-`  
  期望：`200` 整包或 `206` 分段  
- **负例：** 缺 token / 错 token → `403`；未配置 version/url → `404`

---

### 用例 H5 — 指纹校准（可选）

- **方法/URL：** `POST {{base}}/api/v1/locate/bind`
- **Body：**

```json
{
  "wifi_scan": [
    {"mac": "aa:bb:cc:dd:ee:01", "rssi": -40},
    {"mac": "aa:bb:cc:dd:ee:02", "rssi": -55}
  ],
  "lat": 31.2310,
  "lng": 121.4740,
  "accuracy": 20
}
```

- **期望：** `200`，含 `fingerprint`
- **再跑 H4：** 应返回校准后的 lat/lng

---

### 用例 H6 — 下行命令（需 WS 在线，见第 4 节）

- **方法/URL：** `POST {{base}}/api/v1/device/cmd`
- **Body：**

```json
{
  "sn": "{{sn}}",
  "cmd": "reboot",
  "data": {}
}
```

其他可测命令示例（格式以 `docs/嵌入式接口使用文档.md` §6.1 为准）。  
`set_bitrate`：`"0"`=16kbps / `"1"`=24kbps（默认） / `"2"`=32kbps。**只影响**实时翻译音频上行（`type=audio` 的 OPUS Binary 帧）；`record_now` 约 3s 手动录音用固定 32kbps 独立编码器，不受该档位影响。

```json
{"sn":"{{sn}}","cmd":"set_sensitivity","data":"80"}
{"sn":"{{sn}}","cmd":"enable_auto_record","data":"true"}
{"sn":"{{sn}}","cmd":"enable_location","data":"1"}
{"sn":"{{sn}}","cmd":"set_loc_interval","data":30000}
{"sn":"{{sn}}","cmd":"set_perf_mode","data":1}
{"sn":"{{sn}}","cmd":"set_bitrate","data":"1"}
{"sn":"{{sn}}","cmd":"set_sleep_window","data":"0:6"}
{"sn":"{{sn}}","cmd":"set_timezone","data":"480"}
{"sn":"{{sn}}","cmd":"play_cry","data":"0x65"}
{"sn":"{{sn}}","cmd":"set_fence","data":{"lat":31.23,"lng":121.47,"radius":100}}
{"sn":"{{sn}}","cmd":"ota_update","data":{"url":"https://cdn.example.com/fw.bin","version":"1.2.0","force":true,"size":1048576,"md5":"...","accept_ranges":true}}
{"sn":"{{sn}}","cmd":"bind_device","data":{"pid":"123","breed":"dog"}}
{"sn":"{{sn}}","cmd":"record_now","data":{}}
```

- **`enable_auto_record`（自动录音）**：默认关。下发 `"true"` 后等设备 `type=ack`（`result=0`），再用 H8 查状态应见 `auto_record=true`；关着时设备上行录音会被网关丢弃。完整约定见在线 **设备状态与配置** `/#docs-device-status` §2.1.1。

- **record_now 触发录音（新增 2026-08）**：`{"sn":"{{sn}}","cmd":"record_now","data":{}}` 下发后，WS 侧应立刻收到这条 cmd；设备约 **3s 手动录音**，用**固定 32kbps 独立编码器**（不受 `set_bitrate` 影响），随后按音频协议上送完整 `seq=1..total` + `end=1`；可按 §5 用 H8/H9 查设备状态、或查 `a_emb_translate` 验证入库。

- **设备在线：** ApiPost WS 收消息窗口应看到：

```json
{"type":"cmd","cmd":"reboot","data":{}}
```

- **设备离线：** HTTP 仍 `200`，`data.status="queued"`；指令进 Redis 离线队列；下次该 SN 连上会自动下发。

---

### 用例 H7 — TTS 下发（需 WS 在线）

- **方法/URL：** `POST {{base}}/api/v1/device/tts`
- **Body：**（`opus_data` 为 JSON base64）

```json
{
  "sn": "{{sn}}",
  "opus_data": "RkFLRV9UVEVfRlJBTUU="
}
```

> `RkFLRV9UVEVfRlJBTUU=` 解码为字符串 `FAKE_TTS_FRAME`，仅测通路。

- **期望：** 在线 `200`；离线 `500`（TTS 不入离线队列）
- **WS 侧：** 收到 Binary 消息

---

### 用例 H7.1 — 再次下发翻译 Opus（replay，需 WS 在线）

完整翻译成功后，设备下行 Opus 会写入 `a_emb_translate.device_opus`（列表字段 `has_device_opus=true`）。本接口按翻译记录 id 取出后再次推给设备，无需重跑 AI/百度。

- **管理端：** `POST {{base}}/api/v1/device/tts/replay`
- **App 等价：** `POST {{base}}/api/app/v1/translates/replay`
- **Body：**

```json
{
  "id": 676
}
```

可选覆盖目标 SN：`{"id":676,"sn":"{{sn}}"}`（默认用该记录的 `emb_sn`）。

- **期望：** 在线 `200`，`data.frames` > 0；设备离线或无 `device_opus` 时失败
- **WS 侧：** 收到多帧 TTS（每帧 Text meta + Binary Opus），与正常翻译下发相同
- **说明：** 上线前的旧翻译记录没有 `device_opus`，需重新走一遍录音翻译后再测

详见在线文档 **App音频查询** → §3.4（`/#docs-app-audio`）。

---

### 用例 H8 — 单台设备状态快照（**新增 2026-08**）

- **方法/URL：** `GET {{base}}/api/v1/device/status?sn={{sn}}` 或 POST JSON `{"sn":"{{sn}}"}`
- **期望：** `200`，`data` 为完整状态对象
- **首次激活后、未连接时：** 也能返回 meta 信息 + `online=false`
- **WSS 连上并发了 W2/W3 后再查：** 应能看到 `online=true`、`b`、`rssi`、`pid`、`last_loc_*` 等被刷新
- **`auto_record`：** 默认 `false`；`enable_auto_record` 成功 ACK 或设备 `ping`/`status` 上报后才会变为 `true`。详见 `/#docs-device-status` §2.1.1

响应示例（关键字段）：
```json
{
  "code": 1,
  "data": {
    "sn": "TEST_SN_001",
    "online": true,
    "conn_id": "a1b2c3d4e5f67890",
    "b": 86,
    "rssi": -55,
    "pid": "123",
    "last_report_at": 1720000000000
  }
}
```

---

### 用例 H9 — 批量设备状态列表（**新增 2026-08**）

| 请求方式 | URL / Body | 说明 |
|------|------|------|
| GET | `/api/v1/device/status/list` | **无参默认**：当前网关实例本地全部在线设备 |
| GET | `/api/v1/device/status/list?sns=TEST_SN_001,TEST_SN_002` | 指定 SN 列表（逗号分隔） |
| GET | `/api/v1/device/status/list?sns=SN1,SN2&only_online=1` | 指定列表 + 只保留在线的 |
| POST JSON | `/api/v1/device/status/list` body `{"sns":["SN1","SN2"],"only_online":true}` | 同上，数组更方便 |

- **期望：** `{"code":1,"data":{"total":N,"list":[...]}}`
- **调试提示：** 先开 W1 → 跑 H9 空参数 → 看 `list[0].sn` 是否等于自己刚连的那台

---

### 用例 H10 — BLE STAT 文本 + 状态快照联动（**新增 2026-08**）

用于验证 BLE `CHARGE:<n>` 会落到统一状态 key：

1. 保持 W1 在线
2. 在 WS 侧发送明文 Text：`CHARGE:78`
3. 立刻 H8 查状态：**`b` 变 78、`charge=true`、`last_event="charging"`、`last_report_at` 被刷新**

> 字段定义与所有刷新时机的完整表见 [三方对表 §5.1.3](../三方对表-命令与上报.md)。

---

## 4. WebSocket 用例（ApiPost「WebSocket」请求）

### 用例 W1 — 握手成功

- **URL：**

```text
wss://127.0.0.1:8443/ws?sn={{sn}}&token={{token}}
```

- **期望：** 连接成功
- **网关日志：** `设备上线`
- **再请求 H1：** `online >= 1`

### 用例 W1.1 — 推荐：应用层心跳带状态（统一状态上报通道 A，**新增 2026-08**）

在 W1 连接成功后，周期性发送 Text（25s 为正式频率，联调随便发）：

```json
{"type":"ping","bat":78,"charge":true,"rssi":-62,"bound":1,"fw":"1.0.3","pid":"123","breed":"dog"}
```

- **WS 应立刻收到回包：** `{"type":"pong"}`
- **再跑 H8 查状态：** 应看到 `b=78 / charge=true / rssi=-62 / bound=1 / fw_ver=1.0.3 / pid=123 / breed=dog` + `last_report_at` 被刷新
- **网关日志：** `收到带状态应用层心跳，已刷新状态快照`
- **向下兼容（无字段）：** 发送纯字符串 `ping` 或纯 JSON `{"type":"ping"}`，也能收到 `{"type":"pong"}`，只是状态快照不被刷新（仍用旧值）。
- **⚠️ pid 校验：如果发送 `"pid":"pet_10086" （带 pet_ 前缀），会直接被网关拒绝，提示「纯数字整型串」，按新协议请发送 `"123"` / `"1234"`

### 用例 W1.2 — 独立 `type=status` 帧（统一状态上报通道 B，备选，**新增 2026-08**）

在 W1 连接成功后发送 Text：

```json
{
  "type":"status",
  "bat":55,
  "charge":false,
  "rssi":-70,
  "bound":0,
  "pid":"",
  "event":"sleep",
  "state":"doze",
  "loc_lat":31.23,
  "loc_lng":121.47,
  "loc_acc":50,
  "loc_ts":1720000000000000
}
```

- **WS 侧：** 不回包（type=status 只收不回，和 ping 区分开）
- **再跑 H8 查状态：** `b=55 / charge=false / rssi=-70 / bound=0 / last_event="sleep" / last_state="doze" / last_loc_*=...` 被刷新
- **网关日志：** `设备统一状态上报（status）`

### 用例 W1 负例 — 错误 token

```text
wss://127.0.0.1:8443/ws?sn={{sn}}&token=wrong
```

- **期望：** 握手失败 / HTTP 403，无法 Upgrade

---

### 用例 W2 — 独立定位上报（纯 Text）

连接成功后发送 **Text**：

```json
{
  "type": "locate",
  "sn": "TEST_SN_001",
  "bat": 86,
  "rssi": -55,
  "ts": 1720000000000000,
  "loc_type": 1,
  "wifi_scan": [
    {"mac": "aa:bb:cc:dd:ee:01", "rssi": -40},
    {"mac": "aa:bb:cc:dd:ee:02", "rssi": -55}
  ],
  "loc_ts": 1720000000000000
}
```

- **网关日志：** `定位上报已转发`；若 Kafka 关闭，还有 `noop定位转发`

---

### 用例 W3 — 音频双帧（核心）

**帧 1 — Text：**

```json
{
  "sn": "TEST_SN_001",
  "type": "audio",
  "codec": "opus",
  "seq": 0,
  "total": 3,
  "end": 0,
  "pid": "123",
  "db": 62.5,
  "bat": 86,
  "charge": false,
  "rssi": -55,
  "ts": 1720000000000000,
  "crc": 2485935431
}
```

（`ts` 为绝对 Unix **微秒**；后续分片 `ts` 用相对偏移微秒，如 `20000` 表示 +20ms。`charge` 为 `true`/`false`。）

**帧 2 — Binary（紧接着发）：**

载荷原始字节必须是字符串：

```text
FAKE_OPUS_CHUNK_0
```

（不要多空格、不要 JSON 包一层）

预计算 CRC（IEEE CRC32，十进制写进 JSON 的 `crc`）：

| Binary 内容 | crc（十进制） |
|-------------|---------------|
| `FAKE_OPUS_CHUNK_0` | `2485935431` |
| `FAKE_OPUS_CHUNK_1` | `3811274193` |
| `FAKE_OPUS_CHUNK_2` | `2049064043` |

再发 `seq=1`、`seq=2`（`end=true` 放在最后一片），Binary 对应 CHUNK_1 / CHUNK_2。

- **网关日志：** `音频分片已转发` 或 `noop音频转发`
- **若 ApiPost 不好发 Binary：** JSON 里把 `"crc": 0` 或不写 `crc`，仍可测「双帧配对」；或改用第 6 节 Demo。

**省事写法：** 不填 `crc`（或填 `0`），网关会跳过 CRC 校验，只验证「JSON 后跟 Binary」。

---

### 用例 W4 — 补发去重

对 **相同** `seq/total/ts` 再发一遍 Text+Binary（可加 `"type":"retransmit","retransmit":true`）。

- **期望日志：** `识别到重复分片，跳过`

---

### 用例 W5 — 批量头

先发 Text：

```json
{"type":"batch","count":2}
```

- **期望日志：** `进入批量上传模式`
- 然后再发 2 组 meta+bin（同 W3）

---

### 用例 W6 — 收下行（配合 H6/H7）

1. 保持 W1 连接不关  
2. ApiPost 另开 HTTP 跑 H6 / H7  
3. 在 WS 消息列表看 Text 命令 / Binary TTS

---

## 5. 推荐测试顺序（复制到 ApiPost 目录）

```text
01 GET  /healthz
02 POST /api/device/activate          → 保存 token
03 POST /api/device/locate
04 WS   连接 wss://.../ws?sn&token
05 WS   发送 type=loc
06 WS   发送音频双帧 seq0~2
07 WS   发送补发重复片
08 POST /api/v1/device/cmd            → WS 应收到；响应 status=delivered
09 POST /api/v1/device/tts            → WS 应收到 Binary
09b POST /api/v1/device/tts/replay    → 按 translate id 再次下发已存 Opus
10 GET  /api/v1/device/status?sn=     → H8 单台状态（应有 online=true, b, rssi…）
11 GET  /api/v1/device/status/list    → H9 批量列表（total≥1，list[0].sn==自己）
12 WS   发送 CHARGE:78 → 再 H8 验证   → H10 STAT 刷状态联动
13 POST /api/device/refresh           → 更新 token
14 WS   用旧 token 重连（应失败）
15 WS   用新 token 重连（应成功）
16 断开 WS 后 POST cmd                → H6.2：响应 status=queued / queue_len=1
17 再连 WS                            → flushOfflineCmds；WS 侧应收到离线命令
```

---

## 6. ApiPost 测不全时的补刀：demo_client

若 Binary 发送不顺，用仓库客户端一次覆盖双帧/补发：

```powershell
go run ./cmd/demo_client `
  -host wss://127.0.0.1:8443/ws `
  -sn TEST_SN_001 `
  -token 你的token `
  -insecure
```

然后用 ApiPost 对这个「在线假设备」发 H6/H7，看 Demo 控制台是否打印下行。

---

## 7. 常见失败对照

| 现象 | 排查 |
|------|------|
| HTTPS 证书错误 | ApiPost 忽略 SSL |
| activate 500 / Redis 错 | Redis 地址密码与 yaml 一致，服务已启动 |
| activate HTTP 200 但测试台报失败 | 成功信封是 **`code:1`**；旧测试台误把 1 当失败。用返回的 `data.token` 即可连 WS |
| WS 403 | token 是否 activate 返回值；是否 refresh 后仍用旧 token |
| 定位一直 0,0（真机场景） | 设备要先调 locate；网关 mock 默认非 0 |
| 有 JSON 无音频日志 | 是否紧接着发了 Binary；中间夹了别的消息 |
| CRC 校验失败 | crc 与 Binary 字节不一致；或先置 crc=0 |
| cmd 200 但 WS 没收 | 看 online；sn 是否一致；是否连的是别的环境 |
| TTS 失败「设备离线」 | 先保持 WS 连接再调 TTS |

---

## 8. 和真机对齐时注意

1. 真机握手也是 `wss://.../ws?sn=&token=`  
2. 真机音频同样是 **Text JSON + Binary OPUS**  
3. 下行命令必须是 `{"type":"...","data":...}`  
4. TTS：**一条 Binary = 一帧 60ms OPUS**，不要多帧拼包  
5. 设备可能对 OPUS 做 AES；当前网关透传/未解密，联调阶段先关加密或后续再加解密

---

## 9. 你这次用 ApiPost 的最小闭环

只要下面 4 个绿，本地通路就算通：

1. `activate` 拿到 token  
2. WSS 用 token 连上，`healthz.online=1`  
3. WS 发 `type=loc`，网关有定位日志  
4. HTTP `cmd` 后，WS 收到 `{"type":"...","data":...}`  

音频双帧是加分项；ApiPost Binary 不便时用 `demo_client` 即可。
