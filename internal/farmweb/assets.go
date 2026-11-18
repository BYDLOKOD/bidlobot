package farmweb

import (
	"embed"
	"net/http"
)

//go:embed index.html farm.js farm.css
var assets embed.FS

func serveAsset(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/", "/index.html", "/farm.js", "/farm.css":
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self' https://telegram.org; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'")
		http.FileServerFS(assets).ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}
