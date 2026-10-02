package filesystem

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed web
var embeddedWeb embed.FS

func embeddedStaticHandler() (http.Handler, error) {
	assets, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		return nil, err
	}
	fileServer := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Vue Router owns application routes. Serve index.html for those routes,
		// while keeping real asset paths under the embedded file server.
		if r.URL.Path != "/" && !strings.Contains(strings.TrimPrefix(r.URL.Path, "/"), ".") {
			clone := r.Clone(r.Context())
			clone.URL.Path = "/"
			fileServer.ServeHTTP(w, clone)
			return
		}
		fileServer.ServeHTTP(w, r)
	}), nil
}
