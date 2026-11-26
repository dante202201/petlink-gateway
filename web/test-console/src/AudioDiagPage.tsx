import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { bizOK, httpJSON } from "./api";

type DiagStats = {
  total_sessions: number;
  problem_sessions: number;
  ok_sessions: number;
  incomplete: number;
  high_crc_drop: number;
  seq_gap: number;
  high_dedup: number;
  event_kind: number;
  file_kind: number;
};

type CycleEvent = {
  at: number;
  phase: string;
  detail?: string;
  seq?: number;
  reason?: string;
};

type TranslateInfo = {
  id: number;
  emb_sn?: string;
  sn?: string;
  pid?: string;
  pet_type?: string;
  device_ts: number;
  audio_kind: number;
  audio_kind_label?: string;
  status: string;
  origin_audio_url?: string;
  origin_raw_url?: string;
  origin_raw_frames?: number;
  audio_url?: string;
  translate_text?: string;
  result_says?: string;
  origin_audio_duration?: string;
  error_msg?: string;
  has_device_opus?: boolean;
  createtime?: number;
  resulttime?: number;
};

type AudioCycle = {
  id: string;
  sn: string;
  device_ts: number;
  audio_kind: number;
  kind_label: string;
  fingerprint: string;
  started_at: number;
  ended_at: number;
  updated_at: number;
  device_total: number;
  conn_unique_ok: number;
  conn_recv_ok_hits: number;
  store_chunks: number;
  missing_count: number;
  missing_seqs?: number[];
  drop_crc: number;
  drop_dedup: number;
  drop_accept: number;
  drop_other: number;
  gap_warn: number;
  store_ready: boolean;
  store_incomplete: boolean;
  has_problem: boolean;
  pending_retransmit?: boolean;
  missing_final?: boolean;
  verdict: string;
  problems?: string[];
  pipeline_triggered: boolean;
  full_translate: boolean;
  est_duration_ms?: number;
  frame_ms?: number;
  median_gap_ms?: number;
  toc_ms?: number;
  span_ms?: number;
  wall_ms?: number;
  assemble_chunks?: number;
  origin_url?: string;
  rebuilt?: boolean;
  skip_reason?: string;
  skip_detail?: string;
  events?: CycleEvent[];
  translate?: TranslateInfo;
};

type QueryResult = {
  sn: string;
  global_stats: DiagStats;
  device_stats: DiagStats;
  items: AudioCycle[];
  count: number;
  total_for_sn: number;
};

const SN_KEY = "petlink_audio_diag_sn";

function fmtTS(sec: number): string {
  if (!sec) return "-";
  return new Date(sec * 1000).toLocaleString();
}

function kindLabel(c: AudioCycle): string {
  const t = c.translate;
  if (t?.audio_kind_label) return t.audio_kind_label;
  if (c.kind_label) return c.kind_label;
  return c.audio_kind === 1 ? "file" : "event";
}

function alignText(c: AudioCycle): string {
  const tot = c.device_total || 0;
  const store = c.store_chunks || 0;
  if (tot <= 0 && store <= 0) return "无帧统计";
  if (tot <= 0) return `入库 ${store} 帧`;
  if (store >= tot) return `${store}/${tot} 齐`;
  if (c.pending_retransmit) return `${store}/${tot} 待补发`;
  if (c.missing_final) return `${store}/${tot} 最终缺${tot - store}`;
  return `${store}/${tot} 缺${tot - store}`;
}

function statusPill(c: AudioCycle): { text: string; bad: boolean; warn?: boolean } {
  if (c.pending_retransmit) return { text: "待补发", bad: false, warn: true };
  if (c.missing_final || c.has_problem) return { text: "问题", bad: true };
  return { text: "OK", bad: false };
}

function playURL(c: AudioCycle): string {
  return (
    c.translate?.origin_audio_url ||
    c.origin_url ||
    c.translate?.audio_url ||
    ""
  );
}

function ttsURL(c: AudioCycle): string {
  return c.translate?.audio_url || "";
}

/** 输入设备号 → 查询上传诊断 + 入库后 URL/类型，可直接试听 */
export default function AudioDiagPage() {
  const [snInput, setSnInput] = useState(() => localStorage.getItem(SN_KEY) || "");
  const [queriedSN, setQueriedSN] = useState("");
  const [problemsOnly, setProblemsOnly] = useState(false);
  const [result, setResult] = useState<QueryResult | null>(null);
  const [selected, setSelected] = useState<AudioCycle | null>(null);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");
  const [searched, setSearched] = useState(false);
  const [purgeDays, setPurgeDays] = useState("7");
  const [purgeResetStats, setPurgeResetStats] = useState(false);

  const runQuery = async (snRaw: string, onlyProblems: boolean) => {
    const sn = snRaw.trim();
    if (!sn) {
      setMsg("请输入设备号 SN");
      return;
    }
    setBusy(true);
    setMsg("");
    setSelected(null);
    try {
      localStorage.setItem(SN_KEY, sn);
      const q = new URLSearchParams({ sn, limit: "100" });
      if (onlyProblems) q.set("problems_only", "1");
      const { status, data, raw, code, msg: m } = await httpJSON<QueryResult>(
        `/api/v1/audio/diag/query?${q}`,
      );
      if (!bizOK(status, code) || !data) {
        setMsg(`查询失败 HTTP ${status}: ${m || raw}`);
        setResult(null);
        setSearched(true);
        return;
      }
      setResult(data);
      setQueriedSN(data.sn || sn);
      setSearched(true);
      if (!data.items?.length) {
        setMsg(
          data.total_for_sn === 0
            ? `设备 ${sn} 暂无上传/翻译记录`
            : `设备 ${sn} 共 ${data.total_for_sn} 段，当前筛选无匹配项`,
        );
      }
    } catch (e) {
      setMsg(`请求失败: ${String(e)}`);
      setResult(null);
      setSearched(true);
    } finally {
      setBusy(false);
    }
  };

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    void runQuery(snInput, problemsOnly);
  };

  const purge = async () => {
    const days = Number(purgeDays);
    if (!Number.isFinite(days) || days < 0) {
      setMsg("清除天数须为 >=0 的整数（0=清除全部诊断）");
      return;
    }
    const tip =
      days === 0
        ? "确认清除全部音频诊断 Redis 记录？"
        : `确认清除 ${days} 天前的音频诊断记录？`;
    if (!window.confirm(tip)) return;
    setBusy(true);
    setMsg("");
    try {
      const q = new URLSearchParams({ days: String(Math.floor(days)) });
      if (purgeResetStats) q.set("reset_stats", "1");
      const { status, data, raw, code, msg: m } = await httpJSON<{
        removed: number;
        days: number;
        reset_stats: boolean;
      }>(`/api/v1/audio/diag/purge?${q}`, { method: "POST" });
      if (!bizOK(status, code) || !data) {
        setMsg(`清除失败 HTTP ${status}: ${m || raw}`);
        return;
      }
      setMsg(
        `已清除 ${data.removed} 条诊断` +
          (data.reset_stats ? "，并重置全局累计统计" : ""),
      );
      setSelected(null);
      if (queriedSN) void runQuery(queriedSN, problemsOnly);
    } catch (e) {
      setMsg(`清除失败: ${String(e)}`);
    } finally {
      setBusy(false);
    }
  };

  useEffect(() => {
    const saved = localStorage.getItem(SN_KEY);
    if (saved) void runQuery(saved, false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const openDetail = async (c: AudioCycle) => {
    setBusy(true);
    setMsg("");
    try {
      const q = new URLSearchParams({
        sn: c.sn,
        device_ts: String(c.device_ts),
        kind: String(c.audio_kind),
      });
      const { status, data, raw, code, msg: m } = await httpJSON<AudioCycle>(
        `/api/v1/audio/diag/get?${q}`,
      );
      if (!bizOK(status, code) || !data) {
        setMsg(`详情失败 HTTP ${status}: ${m || raw}`);
        setSelected(c);
        return;
      }
      setSelected(data);
    } catch (e) {
      setMsg(`详情失败: ${String(e)}`);
      setSelected(c);
    } finally {
      setBusy(false);
    }
  };

  const ds = result?.device_stats;
  const gs = result?.global_stats;
  const items = result?.items || [];

  return (
    <div className="panel diag-page">
      <form className="diag-search" onSubmit={onSubmit}>
        <label className="diag-search-label">
          设备号 SN
          <input
            className="diag-sn-input"
            value={snInput}
            onChange={(e) => setSnInput(e.target.value)}
            placeholder="输入设备序列号后查询"
            autoFocus
            spellCheck={false}
          />
        </label>
        <label className="check">
          <input
            type="checkbox"
            checked={problemsOnly}
            onChange={(e) => setProblemsOnly(e.target.checked)}
          />
          仅看有问题的段
        </label>
        <button className="btn primary" type="submit" disabled={busy}>
          {busy ? "查询中…" : "查询"}
        </button>
        {queriedSN ? (
          <button
            className="btn"
            type="button"
            disabled={busy}
            onClick={() => void runQuery(queriedSN, problemsOnly)}
          >
            刷新
          </button>
        ) : null}
      </form>

      <div className="diag-search" style={{ alignItems: "center" }}>
        <label className="diag-search-label" style={{ flex: "0 0 auto", minWidth: 120 }}>
          清除诊断
          <input
            className="diag-sn-input"
            style={{ maxWidth: 80, fontSize: "14px" }}
            value={purgeDays}
            onChange={(e) => setPurgeDays(e.target.value)}
            title="天数；0=全部"
          />
        </label>
        <span className="sub" style={{ margin: 0 }}>
          天前的 Redis 记录
        </span>
        <label className="check">
          <input
            type="checkbox"
            checked={purgeResetStats}
            onChange={(e) => setPurgeResetStats(e.target.checked)}
          />
          同时重置全局累计
        </label>
        <button className="btn danger" type="button" disabled={busy} onClick={() => void purge()}>
          清除
        </button>
      </div>

      {msg ? (
        <p
          className="sub"
          style={{
            color:
              msg.includes("暂无") || msg.includes("筛选") ? "var(--muted)" : "var(--err)",
          }}
        >
          {msg}
        </p>
      ) : null}

      {!searched && !result ? (
        <p className="sub">
          输入设备号，对照上传诊断与入库后的原音/TTS URL，并可直接试听。
        </p>
      ) : null}

      {result && ds ? (
        <>
          <section>
            <h3 style={{ margin: "0 0 8px" }}>
              设备 <code>{queriedSN}</code> · 近期诊断 {result.total_for_sn} 段
              {problemsOnly ? `（列表 ${result.count}）` : ` · 列表 ${result.count}`}
            </h3>
            <div className="diag-stats">
              <Stat label="上传段数" value={ds.total_sessions} />
              <Stat label="有问题" value={ds.problem_sessions} warn />
              <Stat label="正常" value={ds.ok_sessions} ok />
              <Stat label="total未对齐" value={ds.incomplete} warn />
              <Stat label="大量CRC舍弃" value={ds.high_crc_drop} warn />
              <Stat label="首传跳号" value={ds.seq_gap} warn />
              <Stat label="大量去重" value={ds.high_dedup} />
              <Stat label="事件流" value={ds.event_kind} />
              <Stat label="文件路" value={ds.file_kind} />
            </div>
          </section>

          {gs ? (
            <details className="diag-global">
              <summary>网关全局累计（所有设备）</summary>
              <div className="diag-stats" style={{ marginTop: 8 }}>
                <Stat label="总会话" value={gs.total_sessions} />
                <Stat label="有问题" value={gs.problem_sessions} warn />
                <Stat label="正常" value={gs.ok_sessions} ok />
                <Stat label="total未对齐" value={gs.incomplete} warn />
                <Stat label="大量CRC舍弃" value={gs.high_crc_drop} warn />
                <Stat label="首传跳号" value={gs.seq_gap} warn />
              </div>
            </details>
          ) : null}

          <div className="grid diag-split">
            <div>
              <h3 style={{ margin: "0 0 8px", fontSize: 15 }}>上传列表</h3>
              <div className="diag-list">
                {items.length === 0 ? (
                  <p className="sub">无列表项</p>
                ) : (
                  items.map((c) => {
                    const origin = playURL(c);
                    const tts = ttsURL(c);
                    const st = c.translate?.status;
                    const pill = statusPill(c);
                    return (
                      <button
                        key={c.id || `${c.sn}:${c.device_ts}:${c.audio_kind}`}
                        type="button"
                        className={`diag-row ${selected?.id === c.id ? "active" : ""} ${pill.bad ? "bad" : ""} ${pill.warn ? "warn" : ""}`}
                        onClick={() => void openDetail(c)}
                      >
                        <div className="diag-row-top">
                          <span className={`pill ${pill.bad ? "off" : pill.warn ? "warn" : "on"}`}>
                            {pill.text}
                          </span>
                          <span className="pill">{kindLabel(c)}</span>
                          {st ? <span className="pill">库:{st}</span> : <span className="pill">未入库</span>}
                          <span className="pill">{alignText(c)}</span>
                          {c.pending_retransmit ? <span className="pill">补发中</span> : null}
                          {c.missing_final ? <span className="pill">最终缺失</span> : null}
                          {c.drop_crc > 0 ? <span className="pill">crc={c.drop_crc}</span> : null}
                          {c.gap_warn > 0 ? <span className="pill">gap={c.gap_warn}</span> : null}
                        </div>
                        <div className="diag-row-mid">
                          <span>{c.fingerprint || "-"}</span>
                          {c.translate?.pet_type ? <span>pet={c.translate.pet_type}</span> : null}
                          {c.translate?.origin_audio_duration ? (
                            <span>{c.translate.origin_audio_duration}s</span>
                          ) : null}
                          {c.translate?.origin_raw_url ? (
                            <span>
                              原帧zip
                              {c.translate.origin_raw_frames
                                ? `=${c.translate.origin_raw_frames}`
                                : ""}
                            </span>
                          ) : null}
                          {c.toc_ms ? (
                            <span>
                              toc={c.toc_ms}ms
                              {c.span_ms ? `/span=${c.span_ms}ms` : ""}
                            </span>
                          ) : null}
                        </div>
                        {c.problems?.length ? (
                          <div className="diag-problems">{c.problems.join("；")}</div>
                        ) : (
                          <div className="diag-problems muted">{c.verdict || "正常"}</div>
                        )}
                        <div
                          className="diag-row-audio"
                          onClick={(e) => e.stopPropagation()}
                          onKeyDown={(e) => e.stopPropagation()}
                        >
                          {origin ? (
                            <AudioBar url={origin} label="原音" />
                          ) : (
                            <span className="muted">暂无原音 URL</span>
                          )}
                          {tts && tts !== origin ? <AudioBar url={tts} label="TTS" /> : null}
                        </div>
                        <div className="diag-row-foot">
                          <span>{fmtTS(c.ended_at || c.translate?.resulttime || c.translate?.createtime || 0)}</span>
                          {c.pipeline_triggered ? <span>已进流水线</span> : null}
                          {c.rebuilt ? <span>已重拼</span> : null}
                          {c.translate?.id ? <span>tid={c.translate.id}</span> : null}
                        </div>
                      </button>
                    );
                  })
                )}
              </div>
            </div>

            <div>
              <h3 style={{ margin: "0 0 8px", fontSize: 15 }}>段详情 / 对照</h3>
              {!selected ? (
                <p className="sub">点左侧一条，对照上传诊断与入库后的音频/译文</p>
              ) : (
                <DetailPane selected={selected} />
              )}
            </div>
          </div>
        </>
      ) : null}
    </div>
  );
}

function DetailPane({ selected }: { selected: AudioCycle }) {
  const tr = selected.translate;
  const origin = playURL(selected);
  const tts = ttsURL(selected);
  return (
    <div className="diag-detail">
      <dl>
        <DT>Fingerprint</DT>
        <dd>
          <code className="fp">{selected.fingerprint}</code>
        </dd>
        <DT>会话</DT>
        <dd>
          <code>
            {selected.sn}:{selected.device_ts}:{selected.audio_kind}
          </code>
          {" · "}
          {kindLabel(selected)}
        </dd>
        <DT>上传结论</DT>
        <dd>{selected.verdict || "-"}</dd>
        <DT>帧长对照（听感「快」先看这里）</DT>
        <dd>
          <strong>
            frame={selected.frame_ms ?? "-"}ms · toc={selected.toc_ms ?? "-"}ms · span=
            {selected.span_ms ?? "-"}ms · median_gap={selected.median_gap_ms ?? "-"}ms · wall=
            {selected.wall_ms ?? "-"}ms
          </strong>
          <div className="sub" style={{ marginTop: 4 }}>
            toc=Opus 内容时长（≈播放时长）；span=设备 chunk_ts 跨度；wall=网关开段→end 墙钟（可含等待，≠音频）。
            {selected.toc_ms && selected.span_ms && selected.span_ms >= selected.toc_ms * 1.25
              ? " 当前 toc≪span → 包间空隙被挤掉，听感偏密/偏快。"
              : selected.toc_ms && selected.wall_ms && selected.wall_ms >= selected.toc_ms * 1.25
                ? " 当前 toc≈span 但 wall 更长 → 文件按 TOC 正常，墙钟含非音频时间。"
                : selected.frame_ms === 60
                  ? " TOC 帧 60ms 符合约定。"
                  : selected.frame_ms === 20
                    ? " TOC=20ms（事件流约定多为 60ms 多帧包）。"
                    : ""}
          </div>
        </dd>
        <DT>未入库原因</DT>
        <dd>
          {selected.skip_reason || selected.skip_detail ? (
            <span style={{ color: "var(--warn)" }}>
              {selected.skip_reason || ""}
              {selected.skip_detail ? ` · ${selected.skip_detail}` : ""}
            </span>
          ) : selected.translate ? (
            "已入库"
          ) : selected.pipeline_triggered ? (
            <span style={{ color: "var(--warn)" }}>
              流水线已触发但无翻译记录（查网关日志：drop_short / encode_fail / oss_fail）
            </span>
          ) : (
            "未触发流水线"
          )}
        </dd>
        <DT>上传问题</DT>
        <dd>
          {selected.problems?.length ? (
            <ul className="diag-ul">
              {selected.problems.map((p) => (
                <li key={p}>{p}</li>
              ))}
            </ul>
          ) : (
            "无"
          )}
        </dd>
        <DT>帧对齐</DT>
        <dd>
          device_total={selected.device_total} · store={selected.store_chunks} · conn_unique=
          {selected.conn_unique_ok} · missing={selected.missing_count}
          {selected.pending_retransmit ? " · 待补发(中间快照)" : ""}
          {selected.missing_final ? " · 最终缺失" : ""}
          {selected.missing_seqs?.length
            ? ` · seqs=[${selected.missing_seqs.slice(0, 24).join(",")}]`
            : ""}
        </dd>
        <DT>舍弃</DT>
        <dd>
          crc={selected.drop_crc} · dedup={selected.drop_dedup} · accept={selected.drop_accept} ·
          other={selected.drop_other} · gap_warn={selected.gap_warn}
        </dd>
        <DT>流水线</DT>
        <dd>
          triggered={String(selected.pipeline_triggered)} · full_translate=
          {String(selected.full_translate)} · incomplete={String(!!selected.store_incomplete)} ·
          rebuilt={String(!!selected.rebuilt)}
          {selected.est_duration_ms != null ? ` · est_ms=${selected.est_duration_ms}` : ""}
        </dd>
      </dl>

      <h4 style={{ margin: "14px 0 6px", fontSize: 13 }}>入库后（a_emb_translate）</h4>
      {!tr ? (
        <p className="sub">尚未写入翻译/原音记录（可能未触发流水线或 OSS 未开）</p>
      ) : (
        <dl>
          <DT>记录 ID</DT>
          <dd>{tr.id}</dd>
          <DT>状态</DT>
          <dd>
            {tr.status}
            {tr.error_msg ? ` · ${tr.error_msg}` : ""}
          </dd>
          <DT>类型</DT>
          <dd>
            {tr.audio_kind_label || (tr.audio_kind === 1 ? "file" : "event")} · pet=
            {tr.pet_type || "-"}
            {tr.origin_audio_duration ? ` · ${tr.origin_audio_duration}s` : ""}
          </dd>
          <DT>译文</DT>
          <dd>{tr.translate_text || tr.result_says || "-"}</dd>
          <DT>原音 URL（可播）</DT>
          <dd>
            {tr.origin_audio_url ? (
              <a href={tr.origin_audio_url} target="_blank" rel="noreferrer">
                {tr.origin_audio_url}
              </a>
            ) : (
              "-"
            )}
          </dd>
          <DT>原帧 zip</DT>
          <dd>
            {tr.origin_raw_url ? (
              <>
                <a href={tr.origin_raw_url} target="_blank" rel="noreferrer">
                  {tr.origin_raw_url}
                </a>
                {tr.origin_raw_frames != null ? (
                  <span className="muted"> · {tr.origin_raw_frames} 帧（无 PLC）</span>
                ) : null}
              </>
            ) : (
              <span className="muted">
                无
                {(tr.audio_kind ?? selected.audio_kind) === 0
                  ? "（事件流应有；若长期为空请确认网关已部署原帧回写修复并重新录一段）"
                  : "（仅事件流会打原帧 zip）"}
              </span>
            )}
          </dd>
          <DT>TTS URL</DT>
          <dd>
            {tr.audio_url ? (
              <a href={tr.audio_url} target="_blank" rel="noreferrer">
                {tr.audio_url}
              </a>
            ) : (
              "-"
            )}
          </dd>
        </dl>
      )}

      <h4 style={{ margin: "14px 0 6px", fontSize: 13 }}>试听</h4>
      <div className="diag-play-box">
        {origin ? <AudioBar url={origin} label="原音（上传拼装）" large /> : <span className="muted">无原音</span>}
        {tts && tts !== origin ? <AudioBar url={tts} label="TTS（翻译合成）" large /> : null}
        {tr?.origin_raw_url ? (
          <p className="sub" style={{ marginTop: 8 }}>
            原帧 zip 为裸 Opus 包归档，浏览器不能直接播；
            <a href={tr.origin_raw_url} target="_blank" rel="noreferrer">
              下载 zip
            </a>
            {tr.origin_raw_frames != null ? `（${tr.origin_raw_frames} 帧）` : ""}
          </p>
        ) : null}
      </div>

      <h4 style={{ margin: "12px 0 6px", fontSize: 13 }}>处理时间线</h4>
      <ul className="diag-events">
        {(selected.events || []).map((ev, i) => (
          <li key={`${ev.at}-${ev.phase}-${i}`}>
            <span className="phase">{ev.phase}</span>
            <span className="at">{fmtTS(ev.at)}</span>
            {ev.reason ? <span className="reason">{ev.reason}</span> : null}
            {ev.detail ? <span className="detail">{ev.detail}</span> : null}
          </li>
        ))}
        {!selected.events?.length ? <li className="muted">无事件</li> : null}
      </ul>
    </div>
  );
}

function AudioBar({
  url,
  label,
  large,
}: {
  url: string;
  label: string;
  large?: boolean;
}) {
  return (
    <div className={`diag-audio ${large ? "large" : ""}`}>
      <div className="diag-audio-head">
        <span>{label}</span>
        <a href={url} target="_blank" rel="noreferrer" onClick={(e) => e.stopPropagation()}>
          打开
        </a>
      </div>
      <audio controls preload="none" src={url} onClick={(e) => e.stopPropagation()} />
    </div>
  );
}

function Stat({
  label,
  value,
  warn,
  ok,
}: {
  label: string;
  value: number;
  warn?: boolean;
  ok?: boolean;
}) {
  return (
    <div className={`diag-stat ${warn ? "warn" : ""} ${ok ? "ok" : ""}`}>
      <div className="n">{value ?? 0}</div>
      <div className="l">{label}</div>
    </div>
  );
}

function DT({ children }: { children: ReactNode }) {
  return <dt>{children}</dt>;
}
