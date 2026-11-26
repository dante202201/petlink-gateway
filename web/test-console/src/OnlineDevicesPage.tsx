import { useCallback, useEffect, useState } from "react";
import { ADMIN_TOKEN_KEY, bizOK, httpJSON } from "./api";

type OnlineDevice = {
  sn: string;
  online: boolean;
  conn_id?: string;
  connected_at?: number;
  token?: string;
  fw_ver?: string;
  b?: number;
  charge?: boolean;
  rssi?: number;
  last_seen_at?: number;
  mac?: string;
  pid?: string;
};

type ListResp = { list: OnlineDevice[]; count: number };

function fmtTime(v?: number) {
  if (!v) return "-";
  const ms = v > 1e12 ? v : v * 1000;
  try {
    return new Date(ms).toLocaleString();
  } catch {
    return String(v);
  }
}

export default function OnlineDevicesPage() {
  const token = localStorage.getItem(ADMIN_TOKEN_KEY) || "";
  const [list, setList] = useState<OnlineDevice[]>([]);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");
  const [force, setForce] = useState(false);
  const [showToken, setShowToken] = useState<Record<string, boolean>>({});

  const authHeaders = useCallback(
    (): HeadersInit => ({
      "X-Admin-Token": token,
    }),
    [token],
  );

  const load = useCallback(async () => {
    const { status, data, raw, code, msg } = await httpJSON<ListResp>("/api/admin/devices/online", {
      headers: authHeaders(),
    });
    if (status === 401) {
      localStorage.removeItem(ADMIN_TOKEN_KEY);
      setMsg("登录已过期，请重新登录");
      return;
    }
    if (status !== 200 || !bizOK(status, code) || !data) {
      setMsg(`读取失败 HTTP ${status}: ${msg || raw}`);
      return;
    }
    setList(data.list || []);
    setMsg("");
  }, [authHeaders]);

  useEffect(() => {
    void load();
    const t = window.setInterval(() => void load(), 5000);
    return () => window.clearInterval(t);
  }, [load]);

  const copy = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      setMsg("已复制");
    } catch {
      setMsg("复制失败，请手动选择");
    }
  };

  const pushOTA = async (sn: string) => {
    if (!confirm(`向 ${sn} 下发当前固件包 OTA？\n目标版本必须高于设备当前 fw（现网多为 2.1.0），否则设备会静默跳过。`)) {
      return;
    }
    setBusy(true);
    setMsg("");
    try {
      const { status, raw, code, msg } = await httpJSON("/api/admin/ota/push", {
        method: "POST",
        headers: authHeaders(),
        body: JSON.stringify({ sn, force }),
      });
      if (status !== 200 || !bizOK(status, code)) {
        setMsg(`下发失败: ${msg || raw}`);
        return;
      }
      setMsg(`已向 ${sn} 下发 ota_update。请到「升级日志」看判定结果。`);
    } catch (e) {
      setMsg(String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="admin-grid" style={{ gridTemplateColumns: "1fr" }}>
      <section className="panel">
        <div className="row" style={{ justifyContent: "space-between", flexWrap: "wrap", gap: 8 }}>
          <h2>在线设备</h2>
          <div className="actions">
            <label className="check">
              <input type="checkbox" checked={force} onChange={(e) => setForce(e.target.checked)} />
              下发时 force=1
            </label>
            <button className="btn" type="button" disabled={busy} onClick={() => void load()}>
              刷新
            </button>
          </div>
        </div>
        <p className="hint">
          本网关当前 WebSocket 在线连接；Token 来自 Redis 鉴权。OTA 使用后台「当前固件」的 url / version。
        </p>
        {list.length === 0 ? (
          <p className="muted">当前没有在线设备。</p>
        ) : (
          <div className="admin-table-wrap">
            <table className="admin-table">
              <thead>
                <tr>
                  <th>SN</th>
                  <th>固件</th>
                  <th>电量 / 信号</th>
                  <th>Token</th>
                  <th>连接</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {list.map((row) => {
                  const tok = row.token || "";
                  const shown = showToken[row.sn];
                  return (
                    <tr key={row.sn}>
                      <td>
                        <div>{row.sn}</div>
                        {row.pid ? <div className="muted">pid {row.pid}</div> : null}
                      </td>
                      <td>{row.fw_ver || "-"}</td>
                      <td>
                        {row.b ?? "-"}%{row.charge ? " 充" : ""} / {row.rssi ?? "-"}
                      </td>
                      <td className="mono break">
                        {tok ? (shown ? tok : `${tok.slice(0, 8)}…`) : "（无）"}
                        {tok ? (
                          <div className="actions" style={{ marginTop: 4, gap: 4 }}>
                            <button
                              className="btn"
                              type="button"
                              onClick={() => setShowToken((p) => ({ ...p, [row.sn]: !p[row.sn] }))}
                            >
                              {shown ? "隐藏" : "显示"}
                            </button>
                            <button className="btn" type="button" onClick={() => void copy(tok)}>
                              复制
                            </button>
                          </div>
                        ) : null}
                      </td>
                      <td>
                        <div>{row.online ? "在线" : "离线"}</div>
                        <div className="muted">{fmtTime(row.connected_at)}</div>
                        <div className="mono muted" style={{ fontSize: "0.8em" }}>
                          {row.conn_id || "-"}
                        </div>
                      </td>
                      <td>
                        <button
                          className="btn primary"
                          type="button"
                          disabled={busy || !row.sn}
                          onClick={() => void pushOTA(row.sn)}
                        >
                          下发 OTA
                        </button>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
        {msg ? <p className="admin-msg">{msg}</p> : null}
      </section>
    </div>
  );
}
