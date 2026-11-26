import path from "node:path";
import fs from "node:fs";
import { fileURLToPath } from "node:url";
import { defineConfig, loadEnv, type Plugin } from "vite";
import react from "@vitejs/plugin-react";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, "../..");
const publicDocsDir = path.join(__dirname, "public", "docs");

const SYNC_DOCS: { src: string; dest: string }[] = [
  {
    src: path.join(repoRoot, "docs", "嵌入式接口使用文档.md"),
    dest: path.join(publicDocsDir, "embedded-api.md"),
  },
  {
    src: path.join(repoRoot, "docs", "三方对表-命令与上报.md"),
    dest: path.join(publicDocsDir, "cmd-table.md"),
  },
  {
    src: path.join(repoRoot, "docs", "设备状态与配置上报.md"),
    dest: path.join(publicDocsDir, "device-status.md"),
  },
  {
    src: path.join(repoRoot, "docs", "App音频查询接口.md"),
    dest: path.join(publicDocsDir, "app-audio-api.md"),
  },
  {
    src: path.join(repoRoot, "docs", "联调手册-ApiPost.md"),
    dest: path.join(publicDocsDir, "debug-manual.md"),
  },
  {
    src: path.join(repoRoot, "docs", "固定叫声play_cry对照表.md"),
    dest: path.join(publicDocsDir, "play-cry.md"),
  },
  {
    src: path.join(repoRoot, "docs", "App-BLE通道协议.md"),
    dest: path.join(publicDocsDir, "app-ble.md"),
  },
  // 文档内嵌图片（Markdown 相对路径 ./xxx.png → /docs/xxx.png）
  {
    src: path.join(repoRoot, "docs", "fire.png"),
    dest: path.join(publicDocsDir, "fire.png"),
  },
];

/** 同步仓库 docs 到 public，便于在线下载 */
function syncRepoDocs(): Plugin {
  const copyAll = () => {
    fs.mkdirSync(publicDocsDir, { recursive: true });
    for (const { src, dest } of SYNC_DOCS) {
      if (fs.existsSync(src)) fs.copyFileSync(src, dest);
    }
  };
  return {
    name: "sync-repo-docs",
    buildStart() {
      copyAll();
    },
    configureServer(server) {
      copyAll();
      for (const { src } of SYNC_DOCS) {
        server.watcher.add(src);
      }
      server.watcher.on("change", (file) => {
        const abs = path.resolve(file);
        for (const { src, dest } of SYNC_DOCS) {
          if (abs === path.resolve(src)) {
            fs.mkdirSync(path.dirname(dest), { recursive: true });
            fs.copyFileSync(src, dest);
          }
        }
      });
    },
  };
}

/** 开发/预览时把 /admin 指到 admin.html；即使未 rewrite，index.html 也会按路径渲染后台。 */
function adminDevRewrite(): Plugin {
  const rewrite = (req: { url?: string }) => {
    const raw = req.url || "";
    const q = raw.indexOf("?");
    const pathname = q >= 0 ? raw.slice(0, q) : raw;
    const search = q >= 0 ? raw.slice(q) : "";
    if (pathname === "/admin" || pathname === "/admin/") {
      req.url = "/admin.html" + search;
    }
  };
  return {
    name: "admin-dev-rewrite",
    configureServer(server) {
      server.middlewares.use((req, _res, next) => {
        rewrite(req);
        next();
      });
    },
    configurePreviewServer(server) {
      server.middlewares.use((req, _res, next) => {
        rewrite(req);
        next();
      });
    },
  };
}

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const gateway = (env.VITE_GATEWAY_ORIGIN || "https://127.0.0.1:8443").replace(/\/$/, "");
  const port = Number(env.VITE_DEV_PORT || 5173);

  const proxyOpts = {
    target: gateway,
    changeOrigin: true,
    secure: false,
  };

  return {
    plugins: [react(), syncRepoDocs(), adminDevRewrite()],
    resolve: {
      alias: {
        "@repo-docs": path.join(repoRoot, "docs"),
      },
    },
    build: {
      rollupOptions: {
        input: {
          main: path.resolve(__dirname, "index.html"),
          admin: path.resolve(__dirname, "admin.html"),
        },
      },
    },
    server: {
      port,
      strictPort: true,
      fs: { allow: [repoRoot] },
      proxy: {
        "/api": proxyOpts,
        "/healthz": proxyOpts,
        "/ws": { ...proxyOpts, ws: true },
      },
    },
  };
});
