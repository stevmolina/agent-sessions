package mcpserver

import (
	"embed"
	"net/http"
	"path"
	"strings"
)

//go:embed web/index.html
var landingHTML []byte

//go:embed web/robots.txt
var robotsTXT []byte

//go:embed web/sitemap.xml
var sitemapXML []byte

//go:embed web/llms.txt
var llmsTXT []byte

//go:embed web/favicon.svg
var faviconSVG []byte

//go:embed web/assets
var assetRoot embed.FS

func staticPage(body []byte, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(body)
	}
}

func registerPublic(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", staticPage(landingHTML, "text/html; charset=utf-8"))
	mux.HandleFunc("GET /robots.txt", staticPage(robotsTXT, "text/plain; charset=utf-8"))
	mux.HandleFunc("GET /sitemap.xml", staticPage(sitemapXML, "application/xml; charset=utf-8"))
	mux.HandleFunc("GET /llms.txt", staticPage(llmsTXT, "text/plain; charset=utf-8"))
	mux.HandleFunc("GET /favicon.svg", staticPage(faviconSVG, "image/svg+xml"))
	mux.HandleFunc("GET /_astro/{name}", serveAsset)
}

func serveAsset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" || name != path.Base(name) || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	data, err := assetRoot.ReadFile("web/assets/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", assetType(name))
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(data)
}

func assetType(name string) string {
	switch {
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".webp"):
		return "image/webp"
	case strings.HasSuffix(name, ".woff2"):
		return "font/woff2"
	case strings.HasSuffix(name, ".svg"):
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}
