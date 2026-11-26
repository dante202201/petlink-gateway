import { useCallback, useEffect, useState } from "react";
import { ADMIN_TOKEN_KEY, bizOK, httpJSON } from "./api";

type OTALog = {
  id: number;
  sn: string;
  from_fw: string;
  target_fw: string;
  url: string;
  force: boolean;
  status: string;
  fail_reason: string;
  disconnected: boolean;
  reconnected: boolean;
  result_fw: string;
  pushed_at: number;
  ack_at: number;
  disconnect_at: number;
  reconnect_at: number;
  boot_at: number;
  result_at: number;
  created_at: number;
};

type ListResp = { list: OTALog[]; total: number };

function fmtTime(v?: number) {
  if (!v) return "-";
  const ms = v > 1e12 ? v : v * 1000;
  try {
    return new Date(ms).toLocaleString();
  } catch {
    return String(v);
  }
}

function statusLabel(s: string) {
  switch (s) {
    case "pushed":
      return "已下发，待 ack";
    case "confirming":
      return "待重启确认";
    case "success":
      return "成功";
    case "fail":
      return "失败";
    default:
      return s || "-";
  }
}

function reasonLabel(s: string) {
  switch (s) {
    case "ack_rejected":
      return "设备拒绝（ack≠0）";
    case "ack_timeout":
      return "未收到 ack";
    case "rollback":
      return "新固件启动失败，已回滚";
    case "timeout_no_reboot":
      return "超时未完成重连（未执行或下载失败）";
    case "skipped":
      return "连接未断开（本地版本已不低于目标）";
    case "superseded":
      return "被新任务覆盖";
    default:
      return s || "-";
  }
}

export default function OTALogsPage() {
  const token = localStorage.getItem(ADMIN_TOKEN_KEY) || "";
  const [list, setList] = useState<OTALog[]>([]);
  const [sn, setSn] = useState("");
  const [status, setStatus] = useState("");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");

  const load = useCallback(async () => {
    const q = new URLSearchParams();
    q.set("limit", "200");
    if (sn.trim()) q.set("sn", sn.trim());
    if (status) q.set("status", status);
    const { status: httpStatus, data, raw, code, msg } = await httpJSON<ListResp>(
      `/api/admin/ota/logs?${q.toString()}`,
      { headers: { "X-Admin-Token": token } },
    );
    if (httpStatus === 401) {
      localStorage.removeItem(ADMIN_TOKEN_KEY);
      setMsg("登录已过期，请重新登录");
      return;
    }
    if (httpStatus !== 200 || !bizOK(httpStatus, code) || !data) {
      setMsg(`读取失败 HTTP ${httpStatus}: ${msg || raw}`);
      return;
    }
    setList(data.list || []);
    setMsg("");
  }, [sn, status, token]);

  useEffect(() => {
    void load();
    const t = window.setInterval(() => void load(), 8000);
    return () => window.clearInterval(t);
  }, [load]);

  return (
    <div className="admin-grid" style={{ gridTemplateColumns: "1fr" }}>
      <section className="panel">
        <div className="row" style={{ justifyContent: "space-between", flexWrap: "wrap", gap: 8 }}>
          <h2>设备升级日志</h2>
          <button className="btn" type="button" disabled={busy} onClick={() => void load()}>
            刷新
          </button>
        </div>
        <p className="hint">
          推送前记录当前 <code>fw</code>；ack result=0 进入待确认；重连后第一条 ping 的 <code>boot=1</code>{" "}
          与目标版本比对。十分钟内未断线视为静默跳过；断过线但无 boot ping 视为下载/刷写失败。联调台手动下发{" "}
          <code>ota_update</code> 也会写入本列表。
        </p>
        <div className="row" style={{ alignItems: "flex-end" }}>
          <label style={{ minWidth: 180 }}>
            设备 SN
            <input value={sn} onChange={(e) => setSn(e.target.value.trim())} placeholder="可选过滤" />
          </label>
          <label style={{ minWidth: 160 }}>
            状态
            <select value={status} onChange={(e) => setStatus(e.target.value)}>
              <option value="">全部</option>
              <option value="pushed">已下发</option>
              <option value="confirming">待确认</option>
              <option value="success">成功</option>
              <option value="fail">失败</option>
            </select>
          </label>
          <button
            className="btn primary"
            type="button"
            disabled={busy}
            onClick={() => {
              setBusy(true);
              void load().finally(() => setBusy(false));
            }}
          >
            筛选
          </button>
        </div>
        {list.length === 0 ? (
          <p className="muted">暂无记录。在「在线设备」或对本机下发 ota_update 后会出现。</p>
        ) : (
          <div className="admin-table-wrap">
            <table className="admin-table">
              <thead>
                <tr>
                  <th>ID</th>
                  <th>设备</th>
                  <th>版本</th>
                  <th>状态</th>
                  <th>断线/重连</th>
                  <th>时间</th>
                </tr>
              </thead>
              <tbody>
                {list.map((row) => (
                  <tr key={row.id} className={row.status === "success" ? "admin-row-current" : undefined}>
                    <td>{row.id}</td>
                    <td>
                      <div>{row.sn}</div>
                      {row.url ? (
                        <div className="mono break muted" style={{ fontSize: "0.8em" }}>
                          {row.url}
                        </div>
                      ) : null}
                    </td>
                    <td>
                      <div>
                        {row.from_fw || "-"} → {row.target_fw || "-"}
                      </div>
                      <div className="muted">上报 {row.result_fw || "-"}</div>
                    </td>
                    <td>
                      <span className={`ota-badge ota-${row.status}`}>{statusLabel(row.status)}</span>
                      {row.status === "fail" ? <div className="muted">{reasonLabel(row.fail_reason)}</div> : null}
                    </td>
                    <td>
                      {row.disconnected ? "已断" : "未断"} / {row.reconnected ? "已连" : "未连"}
                    </td>
                    <td>
                      <div>下发 {fmtTime(row.pushed_at)}</div>
                      <div className="muted">ack {fmtTime(row.ack_at)}</div>
                      <div className="muted">boot {fmtTime(row.boot_at)}</div>
                      <div className="muted">落定 {fmtTime(row.result_at)}</div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {msg ? <p className="admin-msg">{msg}</p> : null}
      </section>
    </div>
  );
}
