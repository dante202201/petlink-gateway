package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

func (h *AdminHandler) registerStatic(mux *http.ServeMux) {
	dir := h.adminStaticDir()
	serve := func(w http.ResponseWriter, r *http.Request) {
		h.serveAdminIndex(w, r, dir)
	}
	// 无论 dist 是否找到都注册入口，避免 /admin 直接 404。
	mux.HandleFunc("/admin", serve)
	mux.HandleFunc("/admin/", serve)
	mux.HandleFunc("/admin.html", serve)
	if dir != "" {
		mux.Handle("/assets/", http.FileServer(http.Dir(dir)))
		return
	}
	if h.log != nil {
		h.log.Warn("后台管理 /admin 已注册，但未找到前端 dist（页面会提示编译）。请在仓库根目录执行: cd web/test-console && npm run build")
	}
}

func (h *AdminHandler) adminStaticDir() string {
	if h == nil {
		return ""
	}
	var configured string
	if h.cfg != nil {
		configured = strings.TrimSpace(h.cfg.Admin.StaticDir)
	}
	for _, dir := range adminDistCandidates(configured) {
		index := filepath.Join(dir, "admin.html")
		if st, err := os.Stat(index); err == nil && !st.IsDir() {
			if h.log != nil {
				h.log.Info("后台管理页面已托管", zap.String("path", "/admin"), zap.String("dir", dir))
			}
			return dir
		}
	}
	if h.log != nil {
		h.log.Info("未找到 admin.html，/admin 将返回提示页", zap.Strings("tried", adminDistCandidates(configured)))
	}
	return ""
}

func adminDistCandidates(configured string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		abs, err := filepath.Abs(p)
		if err == nil {
			p = abs
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	add(configured)
	add(filepath.Join("web", "test-console", "dist"))
	if wd, err := os.Getwd(); err == nil {
		add(filepath.Join(wd, "web", "test-console", "dist"))
		add(filepath.Join(wd, "..", "web", "test-console", "dist"))
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		add(filepath.Join(dir, "web", "test-console", "dist"))
		add(filepath.Join(dir, "..", "web", "test-console", "dist"))
		add(filepath.Join(dir, "..", "..", "web", "test-console", "dist"))
	}
	return out
}

func (h *AdminHandler) serveAdminIndex(w http.ResponseWriter, r *http.Request, dir string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	if dir != "" {
		http.ServeFile(w, r, filepath.Join(dir, "admin.html"))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(adminFallbackHTML))
}

const adminFallbackHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>PetLink 后台管理</title>
  <style>
    body { font-family: system-ui, sans-serif; background:#12160f; color:#e8eedf; margin:0; padding:48px 24px; }
    main { max-width: 640px; margin: 0 auto; line-height: 1.6; }
    a { color:#c4f082; }
    code { background:#242d1e; padding:2px 6px; border-radius:6px; }
  </style>
</head>
<body>
  <main>
    <h1>后台管理前端尚未托管</h1>
    <p>网关 API 已就绪，但没有找到 <code>web/test-console/dist/admin.html</code>。</p>
    <p>请任选一种方式打开后台：</p>
    <ol>
      <li>本地开发：在 <code>web/test-console</code> 执行 <code>npm run dev</code>，浏览器打开
        <a href="http://127.0.0.1:5173/admin">http://127.0.0.1:5173/admin</a>
        （旧地址 <code>/#admin</code> 也会跳到这里）。</li>
      <li>随网关一起访问：在仓库根目录执行
        <code>cd web/test-console && npm run build</code>，确认 <code>admin.static_dir</code> 指向 dist，然后<strong>重启网关</strong>，再打开本页 <code>/admin</code>。</li>
    </ol>
  </main>
</body>
</html>
`
