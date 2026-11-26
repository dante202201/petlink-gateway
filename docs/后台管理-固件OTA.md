# 后台管理：固件包上传与 OTA 元数据

> 面向对象：运维 / 联调  
> 页面：测试台 **后台管理**（`#admin`）  
> 设备侧约定另见 [ota.txt](./ota.txt)、[嵌入式接口使用文档 §3.4](./嵌入式接口使用文档.md)

---

## 1. 能做什么

| 操作 | 说明 |
|------|------|
| 登录后台 | 密码鉴权，拿到管理 Token |
| **上传固件包** | 选文件 → OSS → 自动算 **size / md5** → **写入历史表** → 标为当前 → 写回 yaml |
| **固件历史列表** | 查看以往上传的包；可「设为当前」或删除（非当前） |
| **改元数据** | 不换包，只改 version / url / force / changelog（同步更新当前历史行） |
| 刷新 | 重新拉取当前配置与列表 |

上传或「设为当前」后，设备通过 `POST /api/device/firmware` **优先读库** `a_emb_firmware.is_current=1`；无库时退回 yaml。

页面拆分：

| 页面 | Hash | 作用 |
|------|------|------|
| 后台管理 | `#admin` | 登录、上传并设为当前、改元数据 |
| 固件历史 | `#firmware` | 历史列表、设为当前、删除 |
---

## 2. 打开页面

1. 启动网关（`admin.enabled` + `database.enabled` + `oss.enabled`）。
2. 启动测试台：`cd web/test-console && npm run dev`
3. 浏览器：
   - 上传发布：`http://127.0.0.1:5173/#admin`
   - 历史列表：`http://127.0.0.1:5173/#firmware`

---

## 3. 前置配置（`config/config.yaml`）

```yaml
oss:
  enabled: true
  endpoint: "oss-cn-beijing.aliyuncs.com"
  access_key_id: "..."
  access_key_secret: "..."
  bucket: "..."
  public_base: "https://....aliyuncs.com"   # 公网可达，设备/CDN 用
  # 固件对象键：firmware/ota/{version}/{文件名}（与音频 object_prefix 无关）

admin:
  enabled: true
  password: "你的管理密码"   # 改完需重启网关
  token_ttl: 24h

firmware:
  version: ""      # 可由后台上传自动写入
  url: ""
  force: false
  size: 0
  md5: ""
  changelog: ""
```

- `firmware` 段也可手改；约 **10s 热加载**，不必重启进程。  
- 后台「上传 / 保存」会自动写回本段。

---

## 4. 页面操作步骤

### 4.1 登录

1. 输入 `admin.password`。
2. 点 **登录**。Token 存在浏览器 `localStorage`（`petlink_admin_token`），过期后需重新登录。

### 4.2 上传并发布（推荐）

1. 填写 **version**（必填，如 `5.7.0`）。
2. 按需勾选 **强制升级**（测试时可勾；下发到设备为 `force: true`，忽略版本比对直接升）。
3. 可选填写 **changelog**。
4. 选择固件文件（**≤ 64MB**）。
5. 点 **上传并发布**。

成功后：

- OSS 直链 `url`，自动 `md5` / `size`
- 历史表新增一条并标为 ★当前
- `config.yaml` 的 `firmware` 段写回

### 4.3 固件历史列表

右侧（或下方）表格展示 `a_emb_firmware`：

- **设为当前**：该包写回 yaml，设备查询立即切到该版本
- **删除**：仅删库记录，不删 OSS 文件；**不能删当前包**（先激活另一条）

库空时首次打开列表，会把 yaml 里已有固件自动灌入一条。

### 4.4 仅改元数据（包已在 CDN/OSS）

1. 填好 **version**、**url**（必填）。
2. 设置 force / changelog。
3. 点 **保存元数据**。  
   （会更新当前历史行的元数据；size/md5 沿用已保存值。）

### 4.5 刷新 / 退出

- **刷新**：重新拉取当前固件 + 历史列表。  
- **退出登录**：清本地 Token。

---

## 5. 字段说明

| 字段 | 来源 | 含义 |
|------|------|------|
| `version` | 手填 | 最新版本号；设备与本地比较（`force=false` 时） |
| `url` | 上传自动 / 手填 | 固件直链（OSS/CDN） |
| `download_url` | 设备查询时网关生成 | `/api/device/firmware/download`，鉴权后 **200/206 文件流**，禁止 302 |
| `download_mode` | 已忽略 | 下载接口不再按该字段跳转 |
| `force` | 勾选 | 设备查询返回布尔 `true`/`false`；测试强制升级勾选即可 |
| `size` | 上传自动 | 字节数；设备下载后校验 |
| `md5` | 上传自动 | 整包 MD5（32 位 hex）；对不上则不刷写 |
| `changelog` | 手填 | 更新说明（可选，设备可不消费） |
| `accept_ranges` | 设备查询固定 `true` | 告知设备可用 Range 续传 |

---

## 6. 管理端 HTTP 接口（页面背后）

鉴权：请求头 `X-Admin-Token: <登录返回的 token>`。

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/api/admin/login` | Body：`{"password":"..."}` → `{token, expires_in}` |
| `GET` | `/api/admin/firmware` | 当前生效固件元数据 |
| `PUT`/`POST` | `/api/admin/firmware` | 更新元数据（version、url 必填） |
| `POST` | `/api/admin/firmware/upload` | `multipart`：`file` + `version` + 可选 `force`/`changelog` → OSS + 入库 + 当前 |
| `GET` | `/api/admin/firmware/list` | 历史列表 `{list, current_id, total}` |
| `POST` | `/api/admin/firmware/activate` | Body：`{"id":1}` 设为当前并写 yaml |
| `POST` | `/api/admin/firmware/delete` | Body：`{"id":1}` 删除非当前记录 |
---

## 7. 发布后设备怎么用

### 7.1 HTTP（现网主路径）

```http
POST /api/device/firmware
{"sn":"...","token":"...","fw_ver":"当前版本"}
```

返回含 `url`、`download_url`、`version`、`force`、`size`、`md5`、`accept_ranges`。  
下载：`GET /api/device/firmware/download?sn=&token=`，可带 `Range`。

### 7.2 WebSocket `ota_update`

设备连上后也可由管理接口 / 测试台下发升级命令（与 HTTP 查询是另一条路径）。  
**当前 `#admin` 页不会自动推 WS**；在线设备页或 ApiPost：

```http
POST /api/admin/ota/push
{"sn":"...","force":true}
```

或通用命令口：

```http
POST /api/v1/device/cmd
```

下行到设备的报文示例（`force` 为布尔）：

```json
{
  "type": "cmd",
  "cmd": "ota_update",
  "data": {
    "url": "https://petlink.oss-cn-beijing.aliyuncs.com/firmware/ota/2.1.3/PetTranslator.ino.bin",
    "version": "2.1.3",
    "force": true,
    "size": 1953424,
    "md5": "5bc5a39672677f53f9c2f2fd6184b310",
    "accept_ranges": true
  }
}
```

（`download_url` 可选；默认直链模式不下发。扁平格式约定另见 [ota.txt](./ota.txt)。）

---

## 8. 联调建议（对照 ota.txt）

1. 后台上传真实固件，确认 `size`/`md5` 非空。  
2. 测试强制升级：勾选 **force**，或下发时 `force: true`。  
3. 设备查 `/api/device/firmware` 或收到 `ota_update` 后看 **ack result=0**。  
4. 绿慢闪=下载，绿快闪=刷写；md5 错应红闪且不刷分区。  
5. 断点续传：`download_mode=direct` 时 OSS 自身支持 Range；若走网关 download，需 `accept_ranges=true`。
6. **结果上报 / 回滚**：设备 ack → 下载刷写 → 重启 boot=1 上报新 `fw_ver`；网关 `ota_log`：`confirming`→`success`/`fail(rollback)`。验证期与 3 次 bootloop 回滚在设备侧，服务端以 boot 后版本比对判定。
7. **进度重推**：设备上报 `ota_progress`；`stage=result&note=failed` 立刻再推 `ota_update`；done 不涨约 10s 重推；兜底 40s；ack `result=1` 等 10s，`30s` 无 ack 当丢重推。详见 [ota.txt](./ota.txt)。

---

## 9. 常见问题

| 现象 | 排查 |
|------|------|
| 登录失败 | `admin.enabled`、密码是否改过（需重启）；本地是否走 Vite 代理 |
| 上传失败「oss not enabled」 | `oss.enabled: true` 且 AK/Bucket/`public_base` 正确 |
| 上传超限 | 单文件 ≤ 64MB |
| 设备查固件 404 | `firmware.version` / `url` 为空，先上传或保存元数据 |
| 设备校验失败 | size/md5 与包不一致（手改 url 后未重传） |
| 改 yaml 不生效 | 等约 10s 热加载，或看网关日志「固件配置已热加载」 |
| 设备下固件只有几十 KB/s 或断连 | 网关已关 302 并加 `X-Accel-Buffering: no`。若仍慢，查 Nginx 该 location 的 `proxy_buffering`、`limit_rate`、`proxy_send_timeout`（见 README 反代示例）。不要用 curl -L 测，302 已视为失败 |
| 接口显示 pushed 但设备没收到 | `pushed`≠已送达；看 30s 内是否有 ack；`fail_reason=retried` 表示已自动重推 |
| 下载反复 interrupted / result failed | 设备短窗口；网关见 `ota_progress`（`note=failed` 或 done 不涨）会自动重推 `ota_update`（约 10s/40s） |

---

## 10. 相关文件

| 路径 | 说明 |
|------|------|
| `web/test-console/src/AdminPage.tsx` | 后台管理页（含历史列表） |
| `internal/api/admin.go` | 登录 / 元数据 / 上传 / 列表 / 激活 / 删除 |
| `internal/store/firmware.go` | `a_emb_firmware` CRUD |
| `docs/sql/a_emb_firmware.sql` | MySQL 建表脚本（可手工执行） |
| `config/firmware_live.go` | 固件配置热加载与写回 |
| `docs/ota.txt` | 设备 OTA 行为与测试清单 |
