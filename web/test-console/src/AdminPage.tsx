import { useCallback, useEffect, useState } from "react";
import { ADMIN_TOKEN_KEY, apiURL, bizOK, httpJSON } from "./api";

type FirmwareInfo = {
  id?: number;
  version: string;
  url: string;
  force: number;
  size: number;
  md5: string;
  changelog: string;
  filename?: string;
};

type Wrap<T> = { code?: number; data?: T; msg?: string };

const TOKEN_KEY = ADMIN_TOKEN_KEY;

/** 固件上传发布。embedded=true 时由 /admin 外壳登录，不再单独暴露登录框。 */
export default function AdminPage({ embedded = false }: { embedded?: boolean }) {
  const [password, setPassword] = useState("");
  const [token, setToken] = useState(() => localStorage.getItem(TOKEN_KEY) || "");
  const [fw, setFw] = useState<FirmwareInfo | null>(null);
  const [version, setVersion] = useState("");
  const [force, setForce] = useState(false);
  const [changelog, setChangelog] = useState("");
  const [url, setUrl] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");

  const authHeaders = useCallback(
    (): HeadersInit => ({
      "X-Admin-Token": token,
    }),
    [token],
  );

  const loadFirmware = useCallback(async (tok: string) => {
    const { status, data, raw, code, msg } = await httpJSON<FirmwareInfo>("/api/admin/firmware", {
      headers: { "X-Admin-Token": tok },
    });
    if (status === 401) {
      localStorage.removeItem(TOKEN_KEY);
      setToken("");
      setMsg("登录已过期，请重新登录");
      return;
    }
    if (status !== 200 || !bizOK(status, code) || !data) {
      setMsg(`读取固件失败 HTTP ${status}: ${msg || raw}`);
      return;
    }
    setFw(data);
    setVersion(data.version || "");
    setUrl(data.url || "");
    setForce(data.force === 1);
    setChangelog(data.changelog || "");
    setMsg("");
  }, []);

  useEffect(() => {
    if (token) void loadFirmware(token);
  }, [token, loadFirmware]);

  const login = async () => {
    setBusy(true);
    setMsg("");
    try {
      const { status, data, raw, code, msg } = await httpJSON<{ token: string }>("/api/admin/login", {
        method: "POST",
        body: JSON.stringify({ password }),
      });
      if (status !== 200 || !bizOK(status, code) || !data?.token) {
        const tip =
          status === 0
            ? "网络失败：若直连网关 HTTPS，请先用浏览器打开网关地址信任自签证书，或清空 VITE_GATEWAY_ORIGIN 走 Vite 代理"
            : msg || raw;
        setMsg(`登录失败 HTTP ${status}: ${tip}`);
        return;
      }
      localStorage.setItem(TOKEN_KEY, data.token);
      setToken(data.token);
      setPassword("");
    } catch (e) {
      setMsg(
        `${String(e)}\n提示：本地请清空 VITE_GATEWAY_ORIGIN，用 npm run dev 的代理访问；改 admin.password 后需重启网关。`,
      );
    } finally {
      setBusy(false);
    }
  };

  const logout = () => {
    localStorage.removeItem(TOKEN_KEY);
    setToken("");
    setFw(null);
  };

  const saveMeta = async () => {
    if (!version.trim() || !url.trim()) {
      setMsg("version 与 url 必填");
      return;
    }
    setBusy(true);
    setMsg("");
    try {
      const { status, data, raw, code, msg } = await httpJSON<FirmwareInfo>("/api/admin/firmware", {
        method: "PUT",
        headers: authHeaders(),
        body: JSON.stringify({
          version: version.trim(),
          url: url.trim(),
          force: force ? 1 : 0,
          changelog,
          size: fw?.size || 0,
          md5: fw?.md5 || "",
        }),
      });
      if (status !== 200 || !bizOK(status, code)) {
        setMsg(`保存失败: ${msg || raw}`);
        return;
      }
      setFw(data || null);
      setMsg("元数据已保存；设备查询与历史「当前」已同步");
    } catch (e) {
      setMsg(String(e));
    } finally {
      setBusy(false);
    }
  };

  const upload = async () => {
    if (!file) {
      setMsg("请选择固件文件");
      return;
    }
    if (!version.trim()) {
      setMsg("请填写版本号");
      return;
    }
    setBusy(true);
    setMsg("上传中…");
    try {
      const form = new FormData();
      form.append("file", file);
      form.append("version", version.trim());
      form.append("force", force ? "1" : "0");
      form.append("changelog", changelog);
      const res = await fetch(apiURL("/api/admin/firmware/upload"), {
        method: "POST",
        headers: authHeaders(),
        body: form,
      });
      const raw = await res.text();
      let data: Wrap<FirmwareInfo> = {};
      try {
        data = JSON.parse(raw) as Wrap<FirmwareInfo>;
      } catch {
        /* ignore */
      }
      if (res.status !== 200 || !bizOK(res.status, data.code) || !data.data) {
        setMsg(`上传失败 HTTP ${res.status}: ${data.msg || raw}`);
        return;
      }
      setFw(data.data);
      setUrl(data.data.url);
      setFile(null);
      setMsg(`上传成功并已设为当前：${data.data.url}（md5=${data.data.md5}）。可到「固件历史」查看列表。`);
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
        <h2>后台登录</h2>
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
    <div className="admin-grid">
      <section className="panel">
        <div className="row" style={{ justifyContent: "space-between", flexWrap: "wrap", gap: 8 }}>
          <h2>固件上传 / 发布</h2>
          {!embedded ? (
            <div className="actions">
              <button className="btn" type="button" onClick={logout}>
                退出登录
              </button>
            </div>
          ) : null}
        </div>
        <p className="hint">
          上传 → OSS + <code>a_emb_firmware</code>（标为当前）+ 写回 yaml。设备{" "}
          <code>POST /api/device/firmware</code> 优先读库里的当前包。
        </p>

        {fw ? (
          <div className="admin-cur">
            <div>
              <span className="muted">当前版本</span> {fw.version || "-"}
            </div>
            <div className="mono break">
              <span className="muted">URL</span> {fw.url || "-"}
            </div>
            <div>
              <span className="muted">size</span> {fw.size || 0}{" "}
              <span className="muted">md5</span> {fw.md5 || "-"}{" "}
              <span className="muted">force</span> {fw.force}
            </div>
          </div>
        ) : null}

        <label>
          version（必填）
          <input value={version} onChange={(e) => setVersion(e.target.value)} placeholder="5.7.0" />
        </label>
        <label className="check">
          <input type="checkbox" checked={force} onChange={(e) => setForce(e.target.checked)} />
          强制升级 force=1
        </label>
        <label>
          changelog
          <textarea value={changelog} onChange={(e) => setChangelog(e.target.value)} rows={2} />
        </label>

        <h3>上传固件包（OSS）</h3>
        <label>
          固件文件（≤64MB）
          <input type="file" onChange={(e) => setFile(e.target.files?.[0] || null)} />
        </label>
        <div className="actions">
          <button className="btn primary" type="button" disabled={busy || !file} onClick={() => void upload()}>
            上传并设为当前
          </button>
          <button className="btn" type="button" disabled={busy} onClick={() => void loadFirmware(token)}>
            刷新当前
          </button>
        </div>

        <h3>仅改元数据（已有 URL）</h3>
        <label>
          url
          <input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://....bin" />
        </label>
        <div className="actions">
          <button className="btn" type="button" disabled={busy} onClick={() => void saveMeta()}>
            保存元数据
          </button>
        </div>
        {msg ? <p className="admin-msg">{msg}</p> : null}
      </section>

      <section className="panel">
        <h2>说明</h2>
        <ul className="hint" style={{ margin: 0, paddingLeft: "1.2em" }}>
          <li>历史包管理、回退旧版：打开顶部「固件历史」。</li>
          <li>
            前置：<code>oss.enabled</code>、<code>database.enabled</code>、<code>admin.password</code>。
          </li>
          <li>
            建表：<code>docs/sql/a_emb_firmware.sql</code>（或网关自动建表）。
          </li>
        </ul>
      </section>
    </div>
  );
}
