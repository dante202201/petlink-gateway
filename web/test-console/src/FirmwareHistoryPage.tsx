import { useCallback, useEffect, useState } from "react";
import { ADMIN_TOKEN_KEY, bizOK, httpJSON } from "./api";

type FirmwareInfo = {
  id?: number;
  version: string;
  url: string;
  force: number;
  size: number;
  md5: string;
  changelog: string;
  filename?: string;
  is_current?: boolean;
  created_at?: number;
};

type FirmwareListResp = {
  list: FirmwareInfo[];
  current_id: number;
  total: number;
};

const TOKEN_KEY = ADMIN_TOKEN_KEY;

function fmtTime(sec?: number) {
  if (!sec) return "-";
  try {
    return new Date(sec * 1000).toLocaleString();
  } catch {
    return String(sec);
  }
}

/** 固件历史列表。embedded=true 时由 /admin 外壳登录。 */
export default function FirmwareHistoryPage({ embedded = false }: { embedded?: boolean }) {
  const [token, setToken] = useState(() => localStorage.getItem(TOKEN_KEY) || "");
  const [password, setPassword] = useState("");
  const [list, setList] = useState<FirmwareInfo[]>([]);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");

  const authHeaders = useCallback(
    (): HeadersInit => ({
      "X-Admin-Token": token,
    }),
    [token],
  );

  const loadList = useCallback(async (tok: string) => {
    const { status, data, raw, code, msg } = await httpJSON<FirmwareListResp>("/api/admin/firmware/list", {
      headers: { "X-Admin-Token": tok },
    });
    if (status === 401) {
      localStorage.removeItem(TOKEN_KEY);
      setToken("");
      setMsg("登录已过期，请重新登录");
      return;
    }
    if (status === 503) {
      setList([]);
      setMsg(`固件列表不可用（需开启 database）：${msg || raw}`);
      return;
    }
    if (status !== 200 || !bizOK(status, code) || !data) {
      setMsg(`读取列表失败 HTTP ${status}: ${msg || raw}`);
      return;
    }
    setList(data.list || []);
    setMsg("");
  }, []);

  useEffect(() => {
    if (token) void loadList(token);
  }, [token, loadList]);

  const login = async () => {
    setBusy(true);
    setMsg("");
    try {
      const { status, data, raw, code, msg } = await httpJSON<{ token: string }>("/api/admin/login", {
        method: "POST",
        body: JSON.stringify({ password }),
      });
      if (status !== 200 || !bizOK(status, code) || !data?.token) {
        setMsg(`登录失败 HTTP ${status}: ${msg || raw}`);
        return;
      }
      localStorage.setItem(TOKEN_KEY, data.token);
      setToken(data.token);
      setPassword("");
    } catch (e) {
      setMsg(String(e));
    } finally {
      setBusy(false);
    }
  };

  const activate = async (id: number) => {
    setBusy(true);
    setMsg("");
    try {
      const { status, raw, code, msg } = await httpJSON<FirmwareInfo>("/api/admin/firmware/activate", {
        method: "POST",
        headers: authHeaders(),
        body: JSON.stringify({ id }),
      });
      if (status !== 200 || !bizOK(status, code)) {
        setMsg(`设为当前失败: ${msg || raw}`);
        return;
      }
      setMsg(`已设为当前：id=${id}（设备 POST /api/device/firmware 将返回此包）`);
      await loadList(token);
    } catch (e) {
      setMsg(String(e));
    } finally {
      setBusy(false);
    }
  };

  const remove = async (id: number) => {
    if (!confirm(`确认删除固件记录 id=${id}？（不会删除 OSS 文件）`)) return;
    setBusy(true);
    setMsg("");
    try {
      const { status, raw, code, msg } = await httpJSON<{ deleted_id: number }>("/api/admin/firmware/delete", {
        method: "POST",
        headers: authHeaders(),
        body: JSON.stringify({ id }),
      });
      if (status !== 200 || !bizOK(status, code)) {
        setMsg(`删除失败: ${msg || raw}`);
        return;
      }
      setMsg(`已删除 id=${id}`);
      await loadList(token);
    } catch (e) {
      setMsg(String(e));
    } finally {
      setBusy(false);
    }
  };

  if (!token) {
    if (embedded) {
      return (
        <section className="panel">
          <p className="muted">请先登录后台。</p>
        </section>
      );
    }
    return (
      <section className="panel admin-login">
        <h2>固件历史 · 登录</h2>
        {/* <p className="hint">
          与「后台管理」共用 Token。也可在 <code>#admin</code> 登录后回到本页（
          <code>#firmware</code>）。
        </p> */}
        <label>
          管理密码
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") void login();
            }}
            autoComplete="current-password"
          />
        </label>
        <div className="actions">
          <button className="btn primary" type="button" disabled={busy || !password} onClick={() => void login()}>
            登录
          </button>
        </div>
        {msg ? <p className="admin-msg">{msg}</p> : null}
      </section>
    );
  }

  return (
    <div className="admin-grid" style={{ gridTemplateColumns: "1fr" }}>
      <section className="panel">
        <div className="row" style={{ justifyContent: "space-between", flexWrap: "wrap", gap: 8 }}>
          <h2>固件历史列表</h2>
          <div className="actions">
            <button className="btn" type="button" disabled={busy} onClick={() => void loadList(token)}>
              刷新
            </button>
          </div>
        </div>
        <p className="hint">
          数据表 <code>a_emb_firmware</code>。★ 当前包即设备 <code>POST /api/device/firmware</code>{" "}
          返回的版本；「设为当前」会同步写回 yaml。
        </p>
        {list.length === 0 ? (
          <p className="muted">暂无记录。请先在「后台管理」上传固件包。</p>
        ) : (
          <div className="admin-table-wrap">
            <table className="admin-table">
              <thead>
                <tr>
                  <th>ID</th>
                  <th>版本</th>
                  <th>force</th>
                  <th>size</th>
                  <th>md5</th>
                  <th>时间</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {list.map((row) => (
                  <tr key={row.id} className={row.is_current ? "admin-row-current" : undefined}>
                    <td>
                      {row.id}
                      {row.is_current ? " ★" : ""}
                    </td>
                    <td>
                      <div>{row.version}</div>
                      <div className="mono break muted" style={{ fontSize: "0.8em" }}>
                        {row.url}
                      </div>
                      {row.changelog ? <div className="muted">{row.changelog}</div> : null}
                    </td>
                    <td>{row.force}</td>
                    <td>{row.size}</td>
                    <td className="mono">{row.md5 || "-"}</td>
                    <td>{fmtTime(row.created_at)}</td>
                    <td>
                      <div className="actions" style={{ flexWrap: "wrap", gap: 4 }}>
                        {!row.is_current ? (
                          <>
                            <button
                              className="btn primary"
                              type="button"
                              disabled={busy || !row.id}
                              onClick={() => void activate(row.id!)}
                            >
                              设为当前
                            </button>
                            <button className="btn" type="button" disabled={busy || !row.id} onClick={() => void remove(row.id!)}>
                              删除
                            </button>
                          </>
                        ) : (
                          <span className="muted">当前生效</span>
                        )}
                      </div>
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
