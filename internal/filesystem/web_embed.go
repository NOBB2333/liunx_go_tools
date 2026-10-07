package filesystem

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"strings"
)

// 前端产物不入版本库，目录里只提交 keep.txt 占位文件，让这条 embed 在任何时候都成立。
// 构建前端用 ./build.sh、.\build.ps1、make frontend 或 cd web && pnpm run build。
//
//go:embed web
var embeddedWeb embed.FS

// 没有前端产物时所有页面都返回这段提示，比让人对着 404 猜要好。
const frontendMissingPage = `<!doctype html>
<html lang="zh-CN">
<head><meta charset="utf-8"><title>前端未构建</title></head>
<body style="margin:0;padding:48px;background:#101719;color:#e7f2ec;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;line-height:1.7">
<h1 style="margin:0 0 16px;font-size:22px">这个二进制里没有网页界面</h1>
<p style="margin:0 0 8px;color:#9fb3ad">前端产物不随源码提交，需要在项目根目录构建：</p>
<pre style="margin:0 0 16px;padding:12px 16px;background:#1b2426;border:1px solid #2a3538;border-radius:6px;overflow-x:auto">./build.sh          # macOS / Linux，构建前端并交叉编译六个平台
.\build.ps1         # Windows
make frontend       # 只构建前端</pre>
<p style="margin:0;color:#9fb3ad">构建完成后重新编译 Go 二进制即可。</p>
</body>
</html>
`

func embeddedStaticHandler() (http.Handler, error) {
	assets, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		return nil, err
	}
	if _, statErr := fs.Stat(assets, "index.html"); statErr != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, frontendMissingPage)
		}), nil
	}
	fileServer := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Vue Router 负责应用路由：路径里不含 "." 的一律回落到 index.html，
		// 带扩展名的仍然走真实的静态资源。
		if r.URL.Path != "/" && !strings.Contains(strings.TrimPrefix(r.URL.Path, "/"), ".") {
			clone := r.Clone(r.Context())
			clone.URL.Path = "/"
			fileServer.ServeHTTP(w, clone)
			return
		}
		fileServer.ServeHTTP(w, r)
	}), nil
}
