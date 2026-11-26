import { useCallback, useEffect, useState } from "react";
import { ADMIN_TOKEN_KEY, bizOK, httpJSON } from "./api";
import AdminPage from "./AdminPage";
import AudioDiagPage from "./AudioDiagPage";
import FirmwareHistoryPage from "./FirmwareHistoryPage";
import OTALogsPage from "./OTALogsPage";
import OnlineDevicesPage from "./OnlineDevicesPage";

type Tab = "upload" | "history" | "ota" | "devices" | "audio-diag";

function tabFromHash(): Tab {
  if (typeof location === "undefined") return "upload";
  const h = location.hash.replace(/^#/, "");
  if (h === "history" || h === "firmware") return "history";
  if (h === "ota" || h === "logs") return "ota";
  if (h === "devices" || h === "online") return "devices";
  if (h === "audio-diag" || h === "diag") return "audio-diag";
  return "upload";
}

function goAdminPath(hash: Tab) {
  const path = location.pathname.endsWith("/admin.html") ? "/admin.html" : "/admin";
  const url = `${path}#${hash}`;
  if (`${location.pathname}${location.hash}` === url) {
    return;
  }
  history.replaceState(null, "", url);
}

/** 独立后台：固件、升级日志、在线设备。入口 /admin，不挂在联调测试台。 */
export default function AdminApp() {
  const [tab, setTab] = useState<Tab>(tabFromHash);
  const [password, setPassword] = useState("");
  const [token, setToken] = useState(() => localStorage.getItem(ADMIN_TOKEN_KEY) || "");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");

  const goTab = (t: Tab) => {
    setTab(t);
    goAdminPath(t);
  };

  useEffect(() => {
    if (location.pathname === "/" || location.pathname === "") {
      history.replaceState(null, "", `/admin#${tabFromHash()}`);
    }
    const onHash = () => setTab(tabFromHash());
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

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
            ? "网络失败：若直连网关 HTTPS，请先用浏览器打开网关地址信任自签证书"
            : msg || raw;
        setMsg(`登录失败 HTTP ${status}: ${tip}`);
        return;
      }
      localStorage.setItem(ADMIN_TOKEN_KEY, data.token);
      setToken(data.token);
      setPassword("");
    } catch (e) {
      setMsg(String(e));
    } finally {
      setBusy(false);
    }
  };

  const logout = useCallback(() => {
    localStorage.removeItem(ADMIN_TOKEN_KEY);
    setToken("");
  }, []);

  useEffect(() => {
    const onStorage = () => {
      const t = localStorage.getItem(ADMIN_TOKEN_KEY) || "";
      setToken(t);
    };
    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }, []);

  return (
    <div className="app">
      <header className="hero">
        <div className="brand">PetLink 后台管理</div>
        {token ? (
          <>
            <nav className="nav-tabs" aria-label="后台功能">
              <button
                type="button"
                className={`nav-tab ${tab === "upload" ? "active" : ""}`}
                onClick={() => goTab("upload")}
              >
                固件发布
              </button>
              <button
                type="button"
                className={`nav-tab ${tab === "history" ? "active" : ""}`}
                onClick={() => goTab("history")}
              >
                固件历史
              </button>
              <button
                type="button"
                className={`nav-tab ${tab === "ota" ? "active" : ""}`}
                onClick={() => goTab("ota")}
              >
                升级日志
              </button>
              <button
                type="button"
                className={`nav-tab ${tab === "devices" ? "active" : ""}`}
                onClick={() => goTab("devices")}
              >
                在线设备
              </button>
              <button
                type="button"
                className={`nav-tab ${tab === "audio-diag" ? "active" : ""}`}
                onClick={() => goTab("audio-diag")}
              >
                音频诊断
              </button>
              <button type="button" className="nav-tab" onClick={logout}>
                退出
              </button>
            </nav>
            <p className="sub">
              {tab === "upload"
                ? "上传固件包并设为当前；设备查询与 OTA 下发默认用此版本。"
                : tab === "history"
                  ? "历史固件包：设为当前或删除（不能删当前包）。"
                  : tab === "ota"
                    ? "按 ack + 断线重连 + boot=1 ping 判定升级结果，十分钟窗口。"
                    : tab === "audio-diag"
                      ? "按 SN 查上传诊断、可播原音 / 原帧 zip / TTS；与联调台 #audio-diag 同源。"
                      : "当前在线设备、Token 与固件版本；可对单台下发 OTA。"}
            </p>
          </>
        ) : (
          <p className="sub">使用 config.yaml 的 admin.password 登录。本页不经过联调测试台。</p>
        )}
      </header>

      {!token ? (
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
      ) : (
        <>
          {tab === "upload" ? <AdminPage embedded /> : null}
          {tab === "history" ? <FirmwareHistoryPage embedded /> : null}
          {tab === "ota" ? <OTALogsPage /> : null}
          {tab === "devices" ? <OnlineDevicesPage /> : null}
          {tab === "audio-diag" ? <AudioDiagPage /> : null}
        </>
      )}
    </div>
  );
}
