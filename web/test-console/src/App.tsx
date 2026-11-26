import { useEffect, useRef, useState } from "react";
import { bizOK, gatewayWsBase, httpJSON, unwrapData, wsURL, type LogLevel } from "./api";
import { bytesToBase64, crc32IEEE, textToBytes } from "./crc";
import DocsPage from "./DocsPage";
import AudioDiagPage from "./AudioDiagPage";

type Page = "console" | "docs" | "audio-diag";
type LogItem = { id: number; level: LogLevel; text: string; at: string };

const CMD_TYPES = [
  "reboot",
  "set_sensitivity",
  "set_volume",
  "enable_translate",
  "enable_auto_record",
  "enable_location",
  "set_loc_interval",
  "enable_sleep",
  "set_sleep_window",
  "set_timezone",
  "set_perf_mode",
  "set_pet_type",
  "set_bitrate",
  "set_home_wifi",
  "set_record_mode",
  "set_upload_mode",
  "bind_device",
  "unbind_device",
  "ota_update",
  "token_refresh",
  "factory_reset",
  "play_cry",
  "record_now",
  "enter_calib_ble",
];

let logSeq = 0;

export default function App() {
  const [page, setPage] = useState<Page>(() => {
    if (typeof location === "undefined") return "console";
    if (
      location.hash === "#docs" ||
      location.hash === "#docs-cmd" ||
      location.hash === "#docs-device-status" ||
      location.hash === "#docs-play-cry" ||
      location.hash === "#docs-app-audio" ||
      location.hash === "#docs-ble" ||
      location.hash === "#docs-debug"
    )
      return "docs";
    if (location.hash === "#audio-diag") return "audio-diag";
    return "console";
  });
  const [sn, setSn] = useState("TEST_SN_001");
  const [token, setToken] = useState("");
  const [serverUrl, setServerUrl] = useState("");
  const [online, setOnline] = useState<number | null>(null);
  const [wsState, setWsState] = useState<"off" | "on" | "err">("off");
  const [cmdType, setCmdType] = useState("reboot");
  const [cmdData, setCmdData] = useState("");
  const [logs, setLogs] = useState<LogItem[]>([]);
  const wsRef = useRef<WebSocket | null>(null);
  const logBoxRef = useRef<HTMLDivElement | null>(null);

  const goPage = (p: Page) => {
    setPage(p);
    if (p === "docs") {
      if (!location.hash.startsWith("#docs")) location.hash = "docs-device-status";
      return;
    }
    location.hash = p === "console" ? "console" : p;
  };

  useEffect(() => {
    const onHash = () => {
      if (
        location.hash === "#docs" ||
        location.hash === "#docs-cmd" ||
        location.hash === "#docs-device-status" ||
        location.hash === "#docs-play-cry" ||
        location.hash === "#docs-app-audio" ||
        location.hash === "#docs-ble" ||
        location.hash === "#docs-debug"
      )
        setPage("docs");
      else if (location.hash === "#audio-diag") setPage("audio-diag");
      else setPage("console");
    };
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  const pushLog = (level: LogLevel, text: string) => {
    const at = new Date().toLocaleTimeString();
    setLogs((prev) => {
      const next = [...prev, { id: ++logSeq, level, text, at }];
      return next.slice(-300);
    });
  };

  useEffect(() => {
    const el = logBoxRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [logs]);

  useEffect(() => {
    return () => {
      wsRef.current?.close();
    };
  }, []);

  const healthz = async () => {
    try {
      const { status, data } = await httpJSON<{ status: string; online: number }>("/healthz");
      if (status >= 200 && status < 300 && data) {
        setOnline(data.online ?? 0);
        pushLog("ok", `healthz OK online=${data.online}`);
      } else {
        pushLog("err", `healthz HTTP ${status}`);
      }
    } catch (e) {
      pushLog("err", `healthz 失败: ${String(e)}`);
    }
  };

  const activate = async () => {
    try {
      const { status, data, raw, code } = await httpJSON<{ token: string; server_url: string }>(
        "/api/device/activate",
        {
          method: "POST",
          body: JSON.stringify({
            sn,
            fw_ver: "web-console-1.0",
            mac: "11:22:33:44:55:66",
          }),
        },
      );
      // httpJSON 已解信封；兼容旧逻辑再 unwrap 一次
      const inner = (data?.token ? data : unwrapData<{ token: string; server_url: string }>(data)) || data;
      if (status === 200 && bizOK(status, code) && inner?.token) {
        setToken(inner.token);
        const configured = gatewayWsBase();
        const fromServer = inner.server_url || "";
        setServerUrl(configured || fromServer);
        if (configured && fromServer && configured !== fromServer) {
          pushLog(
            "info",
            `activate 返回 server_url=${fromServer}；测试台按 .env 使用 ${configured}`,
          );
        }
        pushLog("ok", `activate OK token=${inner.token.slice(0, 8)}…\n${raw}`);
      } else {
        pushLog("err", `activate 失败 HTTP ${status} code=${code ?? "-"}\n${raw}`);
      }
    } catch (e) {
      pushLog("err", `activate 失败: ${String(e)}`);
    }
  };

  const refreshToken = async () => {
    if (!token) {
      pushLog("err", "请先 activate 获取 token");
      return;
    }
    try {
      const { status, data, raw, code } = await httpJSON<{ token: string }>("/api/device/refresh", {
        method: "POST",
        body: JSON.stringify({ sn, token }),
      });
      const inner = (data?.token ? data : unwrapData<{ token: string }>(data)) || data;
      if (status === 200 && bizOK(status, code) && inner?.token) {
        setToken(inner.token);
        pushLog("ok", `refresh OK\n${raw}`);
      } else {
        pushLog("err", `refresh 失败 HTTP ${status} code=${code ?? "-"}\n${raw}`);
      }
    } catch (e) {
      pushLog("err", `refresh 失败: ${String(e)}`);
    }
  };

  const locate = async () => {
    try {
      const { status, raw } = await httpJSON("/api/device/locate", {
        method: "POST",
        body: JSON.stringify({
          sn,
          wifi_scan: [
            { mac: "aa:bb:cc:dd:ee:01", rssi: -40, ssid: "Home" },
            { mac: "aa:bb:cc:dd:ee:02", rssi: -55 },
          ],
        }),
      });
      pushLog(status === 200 ? "ok" : "err", `locate HTTP ${status}\n${raw}`);
    } catch (e) {
      pushLog("err", `locate 失败: ${String(e)}`);
    }
  };

  const connectWS = () => {
    if (!token) {
      pushLog("err", "请先 activate");
      return;
    }
    wsRef.current?.close();
    const url = wsURL(sn, token);
    pushLog("info", `连接 ${url}`);
    const ws = new WebSocket(url);
    ws.binaryType = "arraybuffer";
    ws.onopen = () => {
      setWsState("on");
      pushLog("ws", "WebSocket 已连接");
      void healthz();
    };
    ws.onclose = (ev) => {
      setWsState("off");
      pushLog("ws", `WebSocket 关闭 code=${ev.code} reason=${ev.reason || "-"}`);
      if (ev.code === 1006) {
        pushLog(
          "err",
          "1006=握手未完成（浏览器不暴露 HTTP 状态）。常见：①宝塔反代未传 Upgrade（/ws 收到 400）；②token 无效（网关 403）；③上游 http/https 指错。请看网关是否有「握手开始」日志，并确认自定义 location ^~ /ws",
        );
      }
    };
    ws.onerror = () => {
      setWsState("err");
      pushLog("err", "WebSocket 错误（多半握手被拒或反代未升级，见下方 1006 说明）");
    };
    ws.onmessage = async (ev) => {
      if (typeof ev.data === "string") {
        pushLog("ws", `← Text ${ev.data}`);
        return;
      }
      const buf = ev.data instanceof ArrayBuffer ? new Uint8Array(ev.data) : new Uint8Array(await (ev.data as Blob).arrayBuffer());
      const preview = new TextDecoder().decode(buf.slice(0, 64));
      pushLog("ws", `← Binary ${buf.length} bytes preview="${preview}"`);
    };
    wsRef.current = ws;
  };

  const disconnectWS = () => {
    wsRef.current?.close();
    wsRef.current = null;
  };

  const sendLoc = () => {
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      pushLog("err", "WS 未连接");
      return;
    }
    const ts = Math.floor(Date.now() / 1000);
    const body = {
      type: "loc",
      sn,
      bat: 86,
      rssi: -55,
      ts,
      loc_type: "wifi",
      loc_lat: 31.2304,
      loc_lng: 121.4737,
      loc_acc: 25,
      loc_speed: 0.1,
      loc_macs: [
        { mac: "aa:bb:cc:dd:ee:01", rssi: -40 },
        { mac: "aa:bb:cc:dd:ee:02", rssi: -55 },
      ],
      loc_ts: ts,
    };
    ws.send(JSON.stringify(body));
    pushLog("ws", `→ loc 已发送`);
  };

  const sendAudio = async () => {
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      pushLog("err", "WS 未连接");
      return;
    }
    // 双帧：Text JSON → 单独一条 Binary = 一个完整裸 Opus 包（建议 60ms，勿多包拼接）
    const baseTS = Date.now() * 1000; // Unix 微秒
    const total = 3;
    const frameGapUs = 60_000; // 与建议帧长 60ms 对齐
    for (let seq = 0; seq < total; seq++) {
      const opus = textToBytes(`FAKE_OPUS_CHUNK_${seq}`);
      const absOrRelTS = seq === 0 ? baseTS : frameGapUs * seq;
      const meta = {
        sn,
        type: "audio",
        codec: "opus",
        seq,
        total,
        end: seq === total - 1 ? 1 : 0,
        pid: "test_pet",
        proto_ver: 1,
        pet: "dog",
        db: 62.5,
        bat: 86,
        charge: false,
        rssi: -55,
        ts: absOrRelTS,
        crc: crc32IEEE(opus),
      };
      ws.send(JSON.stringify(meta));
      ws.send(opus);
      pushLog(
        "ws",
        `→ 双帧 audio seq=${seq}/${total} end=${meta.end} crc=${meta.crc}\n   Text 元数据 + Binary Opus ${opus.length} bytes`,
      );
      await new Promise((r) => setTimeout(r, 120));
    }
  };

  const sendRetrans = () => {
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      pushLog("err", "WS 未连接");
      return;
    }
    const opus = textToBytes("FAKE_OPUS_CHUNK_0");
    const meta = {
      type: "retransmit",
      sn,
      codec: "opus",
      seq: 0,
      total: 3,
      end: 0,
      retransmit: 1,
      ts: Date.now() * 1000,
      crc: crc32IEEE(opus),
      bat: 86,
      pid: "test_pet",
    };
    ws.send(JSON.stringify(meta));
    ws.send(opus);
    pushLog("ws", "→ 双帧 retransmit：Text 元数据 + Binary Opus（预期网关去重）");
  };

  const sendCmd = async () => {
    try {
      let payload: unknown = cmdData;
      if (cmdData === "") payload = {};
      else {
        try {
          payload = JSON.parse(cmdData);
        } catch {
          payload = cmdData;
        }
      }
      const { status, data: resp, raw } = await httpJSON("/api/v1/device/cmd", {
        method: "POST",
        body: JSON.stringify({ sn, cmd: cmdType, data: payload }),
      });
      let extra = "";
      if (status === 200 && cmdType === "ota_update") {
        const rec = resp as { ota_log_id?: number; ota_from_fw?: string; ota_target_fw?: string; ota_status?: string };
        extra = rec?.ota_log_id
          ? `\n已记入升级日志 id=${rec.ota_log_id} ${rec.ota_from_fw || "-"} → ${rec.ota_target_fw || "-"}（${rec.ota_status || "pushed"}），后台「升级日志」可查。`
          : "\n已下发 ota_update。若后台看不到记录，请确认网关已重启且 database.enabled。";
      }
      pushLog(status === 200 ? "ok" : "err", `cmd HTTP ${status}${extra}\n${raw}`);
    } catch (e) {
      pushLog("err", `cmd 失败: ${String(e)}`);
    }
  };

  const sendTTS = async () => {
    try {
      // HTTP 只传 opus_data；网关会拆成下行双帧：Text type=tts + Binary Opus
      const opus = textToBytes("FAKE_TTS_FRAME");
      const { status, raw } = await httpJSON("/api/v1/device/tts", {
        method: "POST",
        body: JSON.stringify({ sn, pid: "test_pet", opus_data: bytesToBase64(opus) }),
      });
      pushLog(
        status === 200 ? "ok" : "err",
        `tts HTTP ${status}（网关→设备双帧：tts 元数据 Text + Opus Binary）\n${raw}`,
      );
    } catch (e) {
      pushLog("err", `tts 失败: ${String(e)}`);
    }
  };

  return (
    <div className="app">
      <header className="hero">
        <div className="brand">PetLink 网关测试台</div>
        <nav className="nav-tabs" aria-label="页面">
          <button
            type="button"
            className={`nav-tab ${page === "console" ? "active" : ""}`}
            onClick={() => goPage("console")}
          >
            联调控制台
          </button>
          <button
            type="button"
            className={`nav-tab ${page === "docs" ? "active" : ""}`}
            onClick={() => goPage("docs")}
          >
            协议文档
          </button>
          <button
            type="button"
            className={`nav-tab ${page === "audio-diag" ? "active" : ""}`}
            onClick={() => goPage("audio-diag")}
          >
            音频诊断
          </button>
        </nav>
        {page === "console" ? (
          <>
            <p className="sub">
              后端 base：{import.meta.env.VITE_GATEWAY_ORIGIN || "(未配置，走相对路径/代理)"}
              。WS：{gatewayWsBase() || "(同页 host)"}。改 .env.local / .env.production 后需重启
              dev 或重新 build。后台管理请打开{" "}
              <a href="/admin" style={{ color: "var(--accent)" }}>
                /admin
              </a>
              。
            </p>
            <div className="row">
              <span className={`pill ${wsState === "on" ? "on" : "off"}`}>
                WS {wsState === "on" ? "已连接" : wsState === "err" ? "异常" : "未连接"}
              </span>
              <span className="pill">online: {online === null ? "-" : online}</span>
              <button className="btn" type="button" onClick={() => void healthz()}>
                刷新 healthz
              </button>
              <button className="btn" type="button" onClick={() => setLogs([])}>
                清空日志
              </button>
            </div>
          </>
        ) : page === "docs" ? (
          <p className="sub">
            六篇分开看，不互相抄全文：设备状态与配置（姿态/上报准绳）、嵌入式接口、三方对表、App 接口、固定叫声、联调手册。文内链接会切到对应篇。旧地址仍可用（
            <code>#docs-device-status</code> / <code>#docs</code> / <code>#docs-cmd</code> / <code>#docs-app-audio</code> / <code>#docs-ble</code> / <code>#docs-play-cry</code> / <code>#docs-debug</code>）。
          </p>
        ) : page === "audio-diag" ? (
          <p className="sub">
            按设备号对照上传诊断与入库后原音/TTS：类型、状态、URL，列表与详情均可直接试听（
            <code>#audio-diag</code>）。
          </p>
        ) : null}
      </header>

      {page === "docs" ? <DocsPage /> : null}
      {page === "audio-diag" ? <AudioDiagPage /> : null}

      {page === "console" ? (
      <div className="grid">
        <section className="panel">
          <h2>设备与 HTTP</h2>
          <label>
            device sn
            <input value={sn} onChange={(e) => setSn(e.target.value.trim())} />
          </label>
          <div className="actions">
            <button className="btn primary" type="button" onClick={() => void activate()}>
              1. Activate
            </button>
            <button className="btn" type="button" onClick={() => void refreshToken()}>
              Refresh Token
            </button>
            <button className="btn" type="button" onClick={() => void locate()}>
              Locate
            </button>
          </div>
          <label>
            token
            <div className="token">{token || "（尚未激活）"}</div>
          </label>
          {serverUrl ? (
            <label>
              server_url
              <div className="token">{serverUrl}</div>
            </label>
          ) : null}

          <h2>WebSocket（模拟硬件 · 音频为双帧：JSON + Opus BIN）</h2>
          <div className="actions">
            <button className="btn primary" type="button" onClick={connectWS} disabled={!token}>
              2. 连接 WS
            </button>
            <button className="btn danger" type="button" onClick={disconnectWS}>
              断开
            </button>
            <button className="btn" type="button" onClick={sendLoc} disabled={wsState !== "on"}>
              发 type=loc
            </button>
            <button className="btn" type="button" onClick={() => void sendAudio()} disabled={wsState !== "on"}>
              发音频双帧×3
            </button>
            <button className="btn" type="button" onClick={sendRetrans} disabled={wsState !== "on"}>
              发补发双帧
            </button>
          </div>

          <h2>下行（服务端 → 设备）</h2>
          <div className="row">
            <label style={{ flex: 1, minWidth: 160 }}>
              cmd type
              <select value={cmdType} onChange={(e) => setCmdType(e.target.value)}>
                {CMD_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {t}
                  </option>
                ))}
              </select>
            </label>
            <label style={{ flex: 1.4, minWidth: 180 }}>
              data（字符串或 JSON）
              <input
                value={cmdData}
                onChange={(e) => setCmdData(e.target.value)}
                placeholder='字符串如 high / 30000；对象如 {"url":"...","version":"1.2.0"}'
              />
            </label>
          </div>
          <div className="actions">
            <button className="btn primary" type="button" onClick={() => void sendCmd()}>
              下发命令
            </button>
            <button className="btn" type="button" onClick={() => void sendTTS()} disabled={wsState !== "on"}>
              下发 TTS 双帧
            </button>
          </div>
        </section>

        <section className="panel">
          <h2>实时日志</h2>
          <div className="log" ref={logBoxRef}>
            {logs.length === 0 ? (
              <div className="item info">等待操作…先确保网关已启动（Redis + go run ./cmd）</div>
            ) : (
              logs.map((l) => (
                <div key={l.id} className={`item ${l.level}`}>
                  <span className="ts">[{l.at}] </span>
                  {l.text}
                </div>
              ))
            )}
          </div>
        </section>
      </div>
      ) : null}
    </div>
  );
}
