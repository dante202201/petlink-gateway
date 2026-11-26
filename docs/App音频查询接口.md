# App 在线接口（音频 + 状态 + 下发命令）

> 设备经 WSS 上报音频后，网关写入数据库（SQLite 或 **MySQL**）。  
> App 用 HTTPS **轮询**拉新数据 + 调网关 HTTP 接口查询设备状态/下发命令。  
> **统一响应格式**：成功 `{"code":1,"data":...}`，失败 `{"code":0,"msg":"..."}`（HTTP 状态头仍是 200/4xx/5xx，可据此分类错误）。  
> 当前为联调版，**未做 App 登录鉴权**，上线前请加鉴权。  
> 更新：2026-09-21（状态查询含 `token`；`motion` 增加 `stand`）  
> 分工：本文只写 App HTTP。姿态枚举见 [设备状态与配置上报 §2.3](./设备状态与配置上报.md)，不要在这里再维护一份。  
> 在线查阅：测试台 **协议文档 → App音频查询**（`/#docs-app-audio`）

---

## 0. 统一响应规则（重要）

```json
// 业务成功
{"code": 1, "data": { ... }}

// 任何失败：参数/鉴权/内部错误；同时 HTTP 状态头会是 400/401/404/500/503
{"code": 0, "msg": "not found"}
```

App 侧判断：
```js
if (resp.code === 1) {
  const data = resp.data; // 正常取结果
} else {
  // 失败：读 HTTP 状态头做分类 + resp.msg 展示
}
```

### 0.1 POST 入参格式（JSON 与 form 都支持）

对外 App **POST** 接口（状态查询、replay、以及管理端同款 `device/cmd` / `device/tts` 等）统一支持：

| Content-Type | 示例 |
|--------------|------|
| `application/json` | `{"id":676,"sn":"PT-xxx"}` |
| `multipart/form-data` | 字段 `id` / `sn` / … |
| `application/x-www-form-urlencoded` | `id=676&sn=PT-xxx` |

漏标 Content-Type 但 body 以 `{` / `[` 开头时，按 JSON 解析。数字字段（如 `id`）也可传字符串 `"676"`。

---

## 配置

```yaml
database:
  enabled: true
  driver: mysql   # 或 sqlite
  dsn: "user:pass@tcp(127.0.0.1:3306)/petlink?charset=utf8mb4&parseTime=true&loc=Local"
```

本地通路可用 SQLite：`driver: sqlite`，`dsn: "./data/petlink.db"`。

启动时自动建表：`audio_chunk`（分片）、`a_emb_sn`（设备绑定，App 侧写入）、`a_emb_translate`（翻译结果）。

---

## 1. 轮询新音频（推荐）

`GET /api/app/v1/audio?sn={设备SN}&since_id={上次 next_since_id}&limit=50`

| 参数 | 说明 |
|------|------|
| sn | 可选；空则全部设备 |
| since_id | **>0** 时只取 `id` 更大的新分片（按 id **升序**） |
| limit | 默认 50，最大 200 |
| offset | 仅在**不传** `since_id`（或 =0）时用于倒序翻页 |

**App 流程：**

1. 首次：`GET .../audio?sn=XX&limit=50`（倒序看最近），记下返回里最大 `id`（或 `next_since_id`）。  
2. 之后每隔几秒：`GET .../audio?sn=XX&since_id={next_since_id}`。  
3. `items` 为空表示暂无新数据；有数据则用新的 `next_since_id` 继续轮询。  
4. 需要播某一片：`GET /api/app/v1/audio/{id}` 取 `opus_base64`。

**响应：**

```json
{
  "code": 1,
  "data": {
    "items": [
      {
        "id": 12,
        "sn": "TEST_SN_001",
        "pid": "123",
        "codec": "opus",
        "seq": 0,
        "total": 3,
        "end": false,
        "opus_len": 17,
        "device_ts": 1720000000000000,
        "received_at": 1720000000123,
        "imu": [
          {"ts": 1720000000000000, "acc": [0.01, 0.0, 1.0], "gyro": [0.5, -0.2, 0.1]}
        ],
        "motion": "walk"
      }
    ],
    "count": 1,
    "limit": 50,
    "offset": 0,
    "since_id": 0,
    "next_since_id": 12
  }
}
```

列表**不含** `opus_base64`，只有 `opus_len`。  
`imu`：本分片时间窗内的 IMU 采样数组（已 decode）；设备未上报或为空时字段省略。单点：`ts`=Unix 微秒，`acc`=g，`gyro`=dps。  
`motion`：本分片上报的运动姿态 `walk`/`run`/`stand`/`prone`/`lie`（走/跑/站/趴/躺）；未上报则省略。枚举见 [设备状态与配置上报 §2.3](./设备状态与配置上报.md)。

---

## 2. 音频详情（含 OPUS）

`GET /api/app/v1/audio/{id}`

```json
{
  "code": 1,
  "data": {
    "id": 12,
    "sn": "TEST_SN_001",
    "pid": "pet_10086",
    "opus_len": 17,
    "opus_base64": "RkFLRV9PUFVT...",
    "imu": [
      {"ts": 1720000000000000, "acc": [0.01, 0.0, 1.0], "gyro": [0.5, -0.2, 0.1]}
    ],
    "motion": "walk"
  }
}
```

解码后为 **单帧裸 OPUS 包**（WSS 入库）。同一次叫声用 `device_ts + seq/total` 组装。`imu` / `motion` 同列表。

> 设备本地录音文件（`.opus` + 2 字节 LE 长度前缀）见 [App-BLE通道协议 §1.2](./App-BLE通道协议.md)，与本接口不同。

---

## 3. 翻译结果轮询（App 主路径）

设备叫声走完 OSS → AI → TTS 后写入 **`a_emb_translate`**。  
**App 应轮询此接口**拿 `origin_audio_url` / `audio_url` / `translate_text`，而不是依赖分片 BLOB。

`GET /api/app/v1/translates?sn=&since_id=&limit=`（`sn` 对应表字段 `emb_sn`）  
详情：`GET /api/app/v1/translates/{id}`

同样用 `next_since_id` 轮询。

### 3.1 关键字段

| 字段 | 说明 |
|------|------|
| `id` | 自增主键；用于 since_id 增量轮询 |
| `status` | `pending`（刚建档/原音处理）→ `translating`（AI/TTS 中）→ `ok`（完成）/ `fail`（失败）|
| （列表默认） | **只返回 `ok` / `fail`**；`pending`/`translating` 先不出。联调可加 `include_processing=1` |
| `origin_audio_url` | **完整录音**的 OSS 地址（可播文件，转码后；= "完整录音上传 + 入库成功"的铁证） |
| `origin_raw_url` | 事件流**原帧 zip**（`meta.json` + `0000.opus`…；无 PLC、不多段拼接）；文件通路可为空 |
| `origin_raw_frames` | 原帧 zip 内真实帧数 |
| `origin_audio_duration` | 完整录音时长（秒，如 `"9.6"`）|
| `translate_text` | 译文；AI 返回的 `pet_says` |
| `result_says` | App 可直接展示的一句话（空译文时会默认填"当前音频未捕获有效猫狗声纹信号"）|
| `audio_url` | 译文语音 TTS OSS 地址（OPUS 格式） |
| `ai_result` | AI 原始返回，**已 decode 为 JSON 对象**（含 `pet_says` / `is_supported` / `message` 等）；空则省略 |
| `has_device_opus` | 是否已保存设备下行 Opus（可再次下发） |
| `device_opus_frames` | 设备 Opus 帧数；无则为 0 / 省略 |
| `error_msg` | `status=fail` 时的原因 |
| `createtime` / `resulttime` | 创建时间 / 回填完成时间（**秒**时间戳）|
| `emb_sn` / `pid` / `user_id` / `emb_id` | 关联设备与用户 |
| `pet_type` | `dog` / `cat` |
| `device_ts` | 设备上报端第一段时间戳（微秒）|
| `audio_kind` | `0`=事件流（实时叫声）`1`=录音文件（record_now / 自动录音）；两路会各写一条 |
| `audio_kind_label` | `event` / `file`（便于展示）|

### 3.2 完整录音成功的判定

- **列表默认不出过程态**：`pending` / `translating` 不会出现在 `GET /api/app/v1/translates`；译完变 `ok`（或 `fail`）后才进入列表。
- **`status = ok` 且 `origin_audio_url` 非空** = 可展示（完整录音已上 OSS；完整翻译时另有译文/TTS）。
- 详情 `GET /api/app/v1/translates/{id}` 仍可按 id 查任意状态（含过程态），便于联调。
- 仅归档通路（不跑 AI）会直接 `pending` → `ok`（无 `translate_text` / `audio_url` 也属正常）。

### 3.3 列表响应示例

```json
{
  "code": 1,
  "data": {
    "items": [
      {
        "id": 5,
        "emb_sn": "PT-68EE8F6557B8",
        "pid": "123",
        "pet_type": "dog",
        "audio_kind": 0,
        "audio_kind_label": "event",
        "status": "ok",
        "origin_audio_url": "https://cdn.example.com/petlink/origin/PT-68EE8F6557B8/1720000000000000.mp3",
        "origin_raw_url": "https://cdn.example.com/petlink/origin_raw/PT-68EE8F6557B8/1720000000000000.raw.zip",
        "origin_raw_frames": 42,
        "origin_audio_duration": "9.6",
        "translate_text": "肚子饿了，快喂我~",
        "result_says": "肚子饿了，快喂我~",
        "audio_url": "https://cdn.example.com/petlink/tts/xxx.mp3",
        "ai_result": {
          "pet_says": "肚子饿了，快喂我~",
          "is_supported": true,
          "message": "ok"
        },
        "has_device_opus": true,
        "device_opus_frames": 42,
        "createtime": 1720000001,
        "resulttime": 1720000015
      }
    ],
    "count": 1,
    "limit": 50,
    "offset": 0,
    "since_id": 0,
    "next_since_id": 5
  }
}
```

### 3.4 再次下发设备 Opus（replay）

完整翻译成功后，网关会把**设备下行裸 Opus 帧**写入 `a_emb_translate.device_opus`（列表里 `has_device_opus=true`）。可用接口再次推给在线设备播放（不必重新走 AI/百度）。

**App：** `POST /api/app/v1/translates/replay`  
**管理端（等价）：** `POST /api/v1/device/tts/replay`

均支持 **JSON / form-data / x-www-form-urlencoded**（漏标 `Content-Type` 但 body 是 `{...}` 也会按 JSON 解析）。`id` 可为数字或字符串。

**JSON：**

```http
POST /api/app/v1/translates/replay
Content-Type: application/json

{"id":676}
```

可选覆盖目标设备：`{"id":676,"sn":"PT-xxx"}`（默认用该记录的 `emb_sn`）。也支持 `{"id":"676"}`。

**form-data / urlencoded：**

```http
POST /api/app/v1/translates/replay
Content-Type: application/x-www-form-urlencoded

id=676
```

或 multipart：`id=676`、可选 `sn=PT-xxx`；亦可用字段名 `translate_id` 代替 `id`。

成功示例：

```json
{
  "code": 1,
  "data": {
    "id": 676,
    "sn": "PT-68EE8F6557B8",
    "frames": 42,
    "translate_text": "肚子饿了，快喂我~",
    "has_device_opus": true,
    "device_opus_frames": 42
  }
}
```

注意：设备须**在线**；`has_device_opus=false` 的旧记录（本功能上线前）无法 replay，需重新走一遍翻译流水线。

---

## 4. 设备状态查询（单台 / 批量）

> 之前文档里让 App 直接调管理端 `/api/v1/device/status`（code 0/1 语义、权限模型都是管理端的），这里 **补两条 App 侧独立接口**：
> - App 「单台设备页」「在线小圆点」统一走 §4.1 — `GET /api/app/v1/device/status`
> - App 「我的设备列表」批量拉电量+在线+rssi 走 §4.2 — `GET /api/app/v1/device/status/list`
>
> 字段完全复用管理端同一套 `DeviceStatus`（在线/电量/坐标/固件 等 18 个字段），**`online: true/false` 直接取，不需要二次判断**。
>
> 管理端原 `/api/v1/device/status` 和 `/api/v1/device/status/list` 仍保留给后台/运维；App **不要混用**（管理端有 only_online=1 返回全网关在线设备的能力，App 侧为避免越权已被禁用，批量查询必须显式传 `sns`）。

### 4.1 单台设备状态（App 推荐）

**路径：** `GET /api/app/v1/device/status?sn={SN}`  
**等价：** `POST /api/app/v1/device/status` + JSON `{"sn":"PT-68EE8F6557B8"}` 或 Form `sn=...`

**请求参数：**

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| sn | string | 是 | 设备 SN（激活时生成的 PT- 前缀） |

**响应字段（和管理端完全一致，无重复别名）：**

| 字段 | 类型 | 说明 |
|---|---|---|
| online | bool | 在线状态 |
| b | int | 电量 0–100 |
| charge | bool | 是否充电中 |
| rssi | int | WiFi dBm |
| fw_ver | string | 固件版本（设备最近一次上报的 `fw`） |
| token | string | 设备当前 token |
| bound | int | 0/1，由 pid+breed 计算 |
| pid / breed | string | 档案 ID / 品种 |
| last_state / state_label / last_event | string | 运行态英文 / 中文（空闲、睡眠…） / 最近事件 |
| in_sleep | bool | 是否休眠中（`last_state`/`last_event` 为 sleep 类 **或** `tier` 为 `light`/`deep`） |
| **tier** | string | 功耗档位：`normal`/`light`/`deep`；未上报为 `""` |
| **tier_label** | string | 正常/浅睡/深睡；与 tier 同步 |
| **volume** | int\|null | 音量 0–100；设备未报且未下发过则为 `null` |
| **sensitivity** | int\|null | 灵敏度 0–100；同上 |
| **sleep_enabled** | bool\|null | 夜间休眠总开关 |
| **sleep_window** | string | 休眠时段，如 `"0:6"`；未设置则为 `""` |
| **`auto_record`** | bool | **自动录音开关**（推荐）；**默认 `false`**；`enable_auto_record` 成功 ACK 或设备上报后更新；`false` 时网关丢弃设备上行录音 |
| translate_enabled | bool | 与 `auto_record` 同义（兼容）；默认同步 |
| loc_interval_ms | int\|null | 定位间隔毫秒 |
| config_source | string | `device`=设备上报，`shadow`=仅 App 下行影子，空=尚未有配置 |
| last_seen_at / connected_at / last_loc_* 等 | - | 见 [三方对表 §5.1.3](./三方对表-命令与上报.md) |

> 配置字段来源：① 设备 `ping`/`status` 上报；② App 调 `set_volume` 等命令后网关记影子。  
> **`auto_record` 例外**：默认 `false`；仅 `enable_auto_record`（或旧名 `enable_translate`）**成功 ACK** 或设备上报后才更新；为 `false` 时网关丢弃设备上行录音。详见 [设备状态与配置 §2.1.1](./设备状态与配置上报.md)（在线 `/#docs-device-status`）。

**响应示例：**

```json
{
  "code": 1,
  "data": {
    "sn": "PT-68EE8F6557B8",
    "fw_ver": "2.0.0",
    "mac": "68:EE:8F:65:57:B8",
    "online": true,
    "conn_id": "a1b2c3d4e5f67890",
    "connected_at": 1720000000000,
    "last_seen_at": 1720000025000,
    "last_report_at": 1720000020000,
    "seconds_since_seen": 3,
    "b": 98,
    "charge": false,
    "rssi": -51,
    "bound": 1,
    "pid": "623",
    "breed": "cat",
    "db": -600,
    "last_event": "idle",
    "last_state": "idle",
    "state_label": "空闲",
    "tier": "normal",
    "tier_label": "正常",
    "in_sleep": false,
    "volume": 65,
    "sensitivity": 80,
    "auto_record": true,
    "translate_enabled": true,
    "sleep_enabled": true,
    "sleep_window": "0:6",
    "loc_interval_ms": 30000,
    "config_source": "device",
    "config_updated_at": 1720000025000,
    "offline_cmd_pending": 0,
    "last_loc_lat": 0,
    "last_loc_lng": 0,
    "activated_at": 1719000000000
  }
}
```

### 4.2 批量查设备状态（App 「我的设备列表」用）

**路径：** `GET /api/app/v1/device/status/list?sns=SN1,SN2,SN3`  
**等价：** `POST /api/app/v1/device/status/list` + JSON `{"sns":["SN1","SN2"]}` 或 Form `sns=SN1,SN2`

**请求参数：**

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| sns | string / []string | 是 | 要查的 SN 列表，最多建议 50 台；超过会拆成多次查（不会报错） |

> ⚠️ **与管理端批量接口的关键差异**：App 侧 `sns` **必填**。不传时一律返回 `{total:0, list:[]}`，**不会**像管理端 `/api/v1/device/status/list` 那样「默认返回当前网关所有在线设备」，避免 App 越权扫全平台 SN。

**响应示例：**

```json
{
  "code": 1,
  "data": {
    "total": 2,
    "list": [
      {
        "sn": "PT-68EE8F6557B8",
        "online": true,
        "b": 98,
        "charge": false,
        "rssi": -51,
        "last_seen_at": 1720000025000
      },
      {
        "sn": "PT-FFFFFFFFFFFF",
        "online": false,
        "b": 0,
        "charge": false,
        "rssi": 0,
        "last_seen_at": 1719999000000
      }
    ]
  }
}
```

**App「我的设备列表」落地建议：**
1. 列表页进入时 **一次** `/api/app/v1/device/status/list?sns=当前列表所有sn`，同时更新每张卡片的 🟢/🔴、电量、信号、最后活跃时间
2. 列表页停留期间，每 20s 轮询一次批量接口（和 §3 `translates` 轮询分开跑，互不阻塞）
3. 单台设备详情页进入时，单独拉一次 `/api/app/v1/device/status?sn=当前SN`

---

## 5. 下发命令（触发录音 / 重启 / 设置等）

### 5.0 查询全部可下发命令

`GET /api/app/v1/device/commands`（与管理端 `GET /api/v1/device/commands` 等价）

返回网关支持的全部 WSS 下行 `cmd`：`cmd`、`data_type`、`category`、`description`、`example`、`example_request`；`play_cry`、`set_bitrate` 另含 `data_options`。App 可据此动态构建设置页，再调用 §5 下发接口。

**码率相关：**

| cmd | data | 说明 |
|-----|------|------|
| `set_bitrate` | `"0"` / `"1"` / `"2"` | 16 / 24（默认） / 32 kbps；**只影响**实时翻译上行（`type=audio` OPUS Binary） |
| `record_now` | `{}` | 约 3s 手动录音；**固定 32kbps**，不受 `set_bitrate` 影响 |

```json
{
  "code": 1,
  "data": {
    "total": 24,
    "send_method": "POST",
    "send_path": "/api/v1/device/cmd",
    "request_body": {
      "sn": "设备序列号",
      "cmd": "命令名，见 list[].cmd",
      "data": "命令参数，类型见 list[].data_type 与 example"
    },
    "list": [
      {
        "cmd": "set_sensitivity",
        "data_type": "string",
        "category": "config",
        "description": "设置麦克风灵敏度，0–100",
        "example": "80",
        "example_request": {"sn": "PT-xxx", "cmd": "set_sensitivity", "data": "80"},
        "ble_code": "0x06",
        "config_key": "sensitivity"
      }
    ]
  }
}
```

`POST /api/v1/device/cmd`

```json
{
  "sn": "PT-68EE8F6557B8",
  "cmd": "set_sensitivity",
  "data": "80"
}
```

支持所有命令（列表见 [三方对表-命令与上报 §1](./三方对表-命令与上报.md#L20)）。当前 App 侧最常用的是 **§5.1 触发录音**。

### 5.1 触发设备录音（record_now）

```json
{
  "sn": "PT-68EE8F6557B8",
  "cmd": "record_now",
  "data": {}
}
```

- 说明：服务端/APP 主动采约 **3s** 设备音；设备用**固定 32kbps 独立编码器**（**不受 `set_bitrate` 影响**，回放音质优先，设计如此），按音频协议上报完整的 `seq=1..total` + 最后一段 `end=1`，网关 end=true 后会自动触发完整 OSS 入库 + 翻译会话（写入 `a_emb_translate`，App 再用 §3 轮询拿结果）。
- `data` 空对象即可，不传 `data` 时服务端会自动补为 `{}`。

### 5.2 进入 BLE 标定（enter_calib_ble）

```json
{
  "sn": "PT-68EE8F6557B8",
  "cmd": "enter_calib_ble",
  "data": {}
}
```

设备 WSS 回执：`{"type":"ack","cmd":"enter_calib_ble","result":0}` 后会**主动离线**，App 改近场 BLE：

1. **先订阅 FFE5**（不订收不到回执）
2. 往 **FFE4 写单字节 `0x0D`**（标定命令仅此一条）
3. FFE5：`CALIB:START` → 约 8s → `CALIB:OK` 或 `CALIB:FAIL`+原因

此时 `/device/status` 会出现 **`offline_reason=calib_ble`**、**`active_offline=true`**（与意外掉线区分）；WSS 重连后清除。完整时序见 [App-BLE通道协议 §4](./App-BLE通道协议.md)（在线 `/#docs-ble`）。

### 5.3 下发结果的判断

返回的 `data` 含以下字段，App 按 `status` 判定即可：

| 字段 | 含义 |
|------|------|
| `cmd` | 命令名 |
| `type` | 固定 `"cmd"` |
| **`status`** | **`"delivered"` = 已放入下行通道（用户看到的"已下发"）** <br> **`"queued"` = 设备离线，已存队列，上线后自动下发** |
| `delivered` | 与 `status="delivered"` 同义（`true/false`）|
| `queued` | 与 `status="queued"` 同义（`true/false`）|
| `queue_len` | 写入后离线队列剩余条数（`status=queued` 时 > 0）|
| `note` | 文案：`"delivered to conn sendch"` / `"device offline, queued"` 等 |

**已下发示例：**
```json
{
  "code": 1,
  "data": {
    "cmd": "record_now",
    "type": "cmd",
    "status": "delivered",
    "delivered": true,
    "queued": false,
    "queue_len": 0,
    "note": "delivered to conn sendch"
  }
}
```

**离线入队示例：**
```json
{
  "code": 1,
  "data": {
    "cmd": "record_now",
    "type": "cmd",
    "status": "queued",
    "delivered": false,
    "queued": true,
    "queue_len": 1,
    "note": "device offline, queued"
  }
}
```

App 最简处理：
```js
if (res.code === 1) {
  if (res.data.status === "delivered") {
    alert("已下发，等待设备上传录音");
  } else if (res.data.status === "queued") {
    alert(`设备离线，已排队 ${res.data.queue_len} 条，设备上线后自动触发录音`);
  }
}
```

> 注意：TTS 接口 `/api/v1/device/tts` 不支持离线入队；TTS 如果设备离线会直接返回 `code=0,msg="设备离线..."`。

---

## 6. 联调 / curl 示例

```bash
# 最近分片
curl -k "https://127.0.0.1:8443/api/app/v1/audio?sn=TEST_SN_001&limit=20"

# 增量轮询
curl -k "https://127.0.0.1:8443/api/app/v1/audio?sn=TEST_SN_001&since_id=12"

# 详情
curl -k "https://127.0.0.1:8443/api/app/v1/audio/12"

# 翻译结果轮询（App 主路径）
curl -k "https://127.0.0.1:8443/api/app/v1/translates?sn=TEST_SN_001&limit=20"

# 触发录音
curl -k -X POST "https://127.0.0.1:8443/api/v1/device/cmd" \
  -H 'Content-Type: application/json' \
  -d '{"sn":"TEST_SN_001","cmd":"record_now","data":{}}'

# 设备状态
curl -k "https://127.0.0.1:8443/api/v1/device/status?sn=TEST_SN_001"
```

测试台发音频双帧后即可入库。`database.enabled` 必须为 true；如果触发翻译会话还需 `oss.enabled=true`。

---

## 7. 说明

1. 分片接口（§1/§2）存的是**分片**，不是整段文件；**App 实际应优先用 §3 `translates` 轮询**拿 `origin_audio_url`（完整录音）。  
2. 网关去重后再写库（同一 sn+seq+device_ts 不重复写）。  
3. 生产建议 **MySQL**；SQLite 仅便于本机。大音频仍走 OSS（翻译流水线）。  
4. 上线前为 App 接口加鉴权。
