import { useEffect, useState } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import embeddedDoc from "@repo-docs/嵌入式接口使用文档.md?raw";
import cmdTableDoc from "@repo-docs/三方对表-命令与上报.md?raw";
import deviceStatusDoc from "@repo-docs/设备状态与配置上报.md?raw";
import playCryDoc from "@repo-docs/固定叫声play_cry对照表.md?raw";
import appAudioDoc from "@repo-docs/App音频查询接口.md?raw";
import appBleDoc from "@repo-docs/App-BLE通道协议.md?raw";
import debugDoc from "@repo-docs/联调手册-ApiPost.md?raw";

type DocId = "device-status" | "embedded" | "cmd-table" | "app-audio" | "app-ble" | "play-cry" | "debug";

const DOC_ORDER: DocId[] = ["device-status", "embedded", "cmd-table", "app-audio", "app-ble", "play-cry", "debug"];

const DOCS: Record<
  DocId,
  {
    title: string;
    blurb: string;
    source: string;
    fallbackBody: string;
    publicPath: string;
    downloadHref: string;
    downloadName: string;
    hash: string;
    file: string;
  }
> = {
  "device-status": {
    title: "设备状态与配置",
    blurb: "姿态枚举（含 stand 站）、ping/status 怎么报、配置影子。这是准绳，其它篇不再抄全文。",
    source: "docs/设备状态与配置上报.md",
    fallbackBody: deviceStatusDoc,
    publicPath: "/docs/device-status.md",
    downloadHref: "/docs/device-status.md",
    downloadName: "设备状态与配置上报.md",
    hash: "#docs-device-status",
    file: "设备状态与配置上报.md",
  },
  embedded: {
    title: "嵌入式接口",
    blurb: "设备对接：握手、音频帧、下行命令、烧录。状态细节看上一篇。",
    source: "docs/嵌入式接口使用文档.md",
    fallbackBody: embeddedDoc,
    publicPath: "/docs/embedded-api.md",
    downloadHref: "/docs/embedded-api.md",
    downloadName: "嵌入式接口使用文档.md",
    hash: "#docs",
    file: "嵌入式接口使用文档.md",
  },
  "cmd-table": {
    title: "三方对表",
    blurb: "命令和字段对表、错误码。只保留对照，不重复写上报时序。",
    source: "docs/三方对表-命令与上报.md",
    fallbackBody: cmdTableDoc,
    publicPath: "/docs/cmd-table.md",
    downloadHref: "/docs/cmd-table.md",
    downloadName: "三方对表-命令与上报.md",
    hash: "#docs-cmd",
    file: "三方对表-命令与上报.md",
  },
  "app-audio": {
    title: "App 接口",
    blurb: "App 拉音频、查状态、下发命令。姿态含义见「设备状态与配置」。",
    source: "docs/App音频查询接口.md",
    fallbackBody: appAudioDoc,
    publicPath: "/docs/app-audio-api.md",
    downloadHref: "/docs/app-audio-api.md",
    downloadName: "App音频查询接口.md",
    hash: "#docs-app-audio",
    file: "App音频查询接口.md",
  },
  "app-ble": {
    title: "App BLE",
    blurb: "近场 BLE：命令字、标定 FFE4/FFE5、配网与分包。含 enter_calib_ble 时序。",
    source: "docs/App-BLE通道协议.md",
    fallbackBody: appBleDoc,
    publicPath: "/docs/app-ble.md",
    downloadHref: "/docs/app-ble.md",
    downloadName: "App-BLE通道协议.md",
    hash: "#docs-ble",
    file: "App-BLE通道协议.md",
  },
  "play-cry": {
    title: "固定叫声",
    blurb: "play_cry 的叫声 ID，独立一篇。",
    source: "docs/固定叫声play_cry对照表.md",
    fallbackBody: playCryDoc,
    publicPath: "/docs/play-cry.md",
    downloadHref: "/docs/play-cry.md",
    downloadName: "固定叫声play_cry对照表.md",
    hash: "#docs-play-cry",
    file: "固定叫声play_cry对照表.md",
  },
  debug: {
    title: "联调手册",
    blurb: "ApiPost 操作步骤。字段含义以另外几篇为准。",
    source: "docs/联调手册-ApiPost.md",
    fallbackBody: debugDoc,
    publicPath: "/docs/debug-manual.md",
    downloadHref: "/docs/debug-manual.md",
    downloadName: "联调手册-ApiPost.md",
    hash: "#docs-debug",
    file: "联调手册-ApiPost.md",
  },
};

function docFromHash(): DocId {
  if (typeof location === "undefined") return "device-status";
  const h = location.hash.split("?")[0];
  for (const id of DOC_ORDER) {
    if (h === DOCS[id].hash) return id;
  }
  if (h === "#docs" || h.startsWith("#docs")) return "embedded";
  return "device-status";
}

function docIdFromHref(href: string | undefined): DocId | null {
  if (!href) return null;
  const raw = decodeURIComponent(href).replace(/^\.\//, "");
  const hash = raw.match(/#(docs(?:-[\w-]+)?)/)?.[1];
  if (hash) {
    for (const id of DOC_ORDER) {
      if (DOCS[id].hash === `#${hash}`) return id;
    }
    if (hash === "docs") return "embedded";
  }
  const file = raw.split("#")[0].split("/").pop() || "";
  if (!file.endsWith(".md")) return null;
  for (const id of DOC_ORDER) {
    if (DOCS[id].file === file) return id;
  }
  return null;
}

export default function DocsPage() {
  const [docId, setDocId] = useState<DocId>(docFromHash);

  useEffect(() => {
    const onHash = () => setDocId(docFromHash());
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  const select = (id: DocId) => {
    setDocId(id);
    const next = DOCS[id].hash;
    if (location.hash !== next) location.hash = next.slice(1);
  };

  const doc = DOCS[docId];
  // 正文以编译期 ?raw 为准（与仓库 docs 同源 UTF-8）；public/docs 仅用于下载，避免旧 public 乱码覆盖显示
  const body = doc.fallbackBody;

  return (
    <div className="docs-wrap">
      <div className="docs-switch" role="tablist" aria-label="文档切换">
        {DOC_ORDER.map((id) => (
          <button
            key={id}
            type="button"
            role="tab"
            aria-selected={docId === id}
            className={`docs-tab ${docId === id ? "active" : ""}`}
            onClick={() => select(id)}
          >
            {DOCS[id].title}
          </button>
        ))}
      </div>
      <p className="docs-blurb">{doc.blurb}</p>
      <div className="docs-toolbar">
        <span className="docs-hint">
          来源：仓库 <code>{doc.source}</code>（编译期内置 UTF-8，与 docs 同步）
        </span>
        <a className="btn" href={doc.downloadHref} download={doc.downloadName}>
          下载 Markdown
        </a>
      </div>
      <article className="docs-md">
        <ReactMarkdown
          remarkPlugins={[remarkGfm]}
          components={{
            a: ({ href, children }) => {
              const target = docIdFromHref(href);
              if (target) {
                return (
                  <a
                    href={DOCS[target].hash}
                    onClick={(e) => {
                      e.preventDefault();
                      select(target);
                    }}
                  >
                    {children}
                  </a>
                );
              }
              const external = !!href && /^https?:\/\//i.test(href);
              return (
                <a href={href} target={external ? "_blank" : undefined} rel={external ? "noreferrer" : undefined}>
                  {children}
                </a>
              );
            },
            img: ({ src, alt }) => {
              let href = src || "";
              // ./fire.png / fire.png → /docs/fire.png（与 public/docs 同步）
              if (href && !/^https?:\/\//i.test(href) && !href.startsWith("/")) {
                href = "/docs/" + href.replace(/^\.\//, "");
              }
              return <img src={href} alt={alt || ""} loading="lazy" />;
            },
          }}
        >
          {body}
        </ReactMarkdown>
      </article>
    </div>
  );
}
