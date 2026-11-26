export type LogLevel = "info" | "ok" | "err" | "ws";

/** 后台管理登录 Token（localStorage）。 */
export const ADMIN_TOKEN_KEY = "petlink_admin_token";

/**
 * 完整网关 base URL（VITE_GATEWAY_ORIGIN）。
 * 原样使用，代码不追加端口；未配置则走相对路径 / 当前页主机。
 */
export function gatewayOrigin(): string {
  return (import.meta.env.VITE_GATEWAY_ORIGIN || "").replace(/\/$/, "");
}

/** 由配置推导的 WSS 入口（不含 query），供展示与连接。 */
export function gatewayWsBase(): string {
  const base = gatewayOrigin();
  if (!base) return "";
  const u = new URL(base);
  const proto = u.protocol === "https:" ? "wss" : "ws";
  return `${proto}://${u.host}/ws`;
}

/** HTTP：配置了 VITE_GATEWAY_ORIGIN 则直连；否则相对路径（Vite/Nginx 代理）。 */
export function apiURL(path: string): string {
  const p = path.startsWith("/") ? path : `/${path}`;
  const base = gatewayOrigin();
  return base ? `${base}${p}` : p;
}

/** WebSocket：优先用 VITE_GATEWAY_ORIGIN；未配置则用当前页面 host。 */
export function wsURL(sn: string, token: string): string {
  const q = `sn=${encodeURIComponent(sn)}&token=${encodeURIComponent(token)}`;
  const wsBase = gatewayWsBase();
  if (wsBase) {
    return `${wsBase}?${q}`;
  }
  const proto = location.protocol === "https:" ? "wss" : "ws";
  return `${proto}://${location.host}/ws?${q}`;
}

export async function httpJSON<T = unknown>(
  path: string,
  init?: RequestInit,
): Promise<{ status: number; data: T; raw: string; code?: number; msg?: string }> {
  const res = await fetch(apiURL(path), {
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...(init?.headers || {}),
    },
  });
  const raw = await res.text();
  let parsed: unknown = {};
  try {
    parsed = raw ? JSON.parse(raw) : {};
  } catch {
    parsed = { raw };
  }

  // 网关统一信封 { code, data, msg } → 直接返回内层 data，避免调用方再解一层
  let code: number | undefined;
  let msg: string | undefined;
  let data = parsed as T;
  if (parsed && typeof parsed === "object" && !Array.isArray(parsed) && "data" in (parsed as object)) {
    const env = parsed as { code?: number; data?: T; msg?: string };
    code = env.code;
    msg = env.msg;
    if (env.data !== undefined) {
      data = env.data;
    }
  }

  return { status: res.status, data, raw, code, msg };
}

/** 业务成功：HTTP 2xx 且信封 code 为 1（或无 code 的裸 JSON）。失败为 code=0。 */
export function bizOK(status: number, code?: number): boolean {
  if (status < 200 || status >= 300) return false;
  return code === undefined || code === 1;
}

/** 网关统一信封：{ code, data, msg? } → 取出 data；非信封则原样返回。 */
export function unwrapData<T = unknown>(body: unknown): T | undefined {
  if (body == null || typeof body !== "object") return undefined;
  const o = body as { code?: number; data?: T };
  if ("data" in o && o.data !== undefined) return o.data;
  return body as T;
}
