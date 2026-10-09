// Package webui serves the management UI on explicit page routes.
package webui

import (
	"embed"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"gpt-load/internal/platform/config"
)

const (
	distRoot = "dist"
	// Reka UI 的下拉视口会注入固定样式，只按内容哈希放行。
	// 升级依赖时需核对 SelectViewport / ComboboxViewport 的样式文本。
	indexCSP = "default-src 'self'; script-src 'self'; style-src 'self'; " +
		"style-src-elem 'self' " +
		"'sha256-60LHlRjW/B3CtzIoE/Lf1/NEDvko9efWMFaGVhHu/cs=' " +
		"'sha256-0sLsI2a+NIcumVvBF9zD/ArGqlZR2xfnxsALPmK7nj8='; " +
		"style-src-attr 'unsafe-inline'; " +
		"img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; " +
		"base-uri 'self'; frame-ancestors 'none'; form-action 'self'"
	fallbackIndex = `<!doctype html>
<html lang="zh-CN">
<head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>GPT-Load</title></head>
<body><main><h1>GPT-Load</h1><p>前端资源尚未构建，请运行 make build。</p></main></body>
</html>`
)

//go:embed all:dist
var embeddedFiles embed.FS

// Server serves immutable assets and the SPA index for known UI routes.
type Server struct {
	files fs.FS
	root  string
	// index caches the embedded SPA entry point; nil reads it from files per
	// request so an external dist directory stays replaceable without a
	// restart. A nil index marks that lazy (external) mode: failing to read
	// it at request time answers 500 instead of the compile-time fallback,
	// which would wrongly tell the operator to run "make build".
	index []byte
	pages []pageRoute
}

// NewServer creates the UI server. A non-empty WEB_DIST_DIR serves that dist
// directory instead of the embedded build.
func NewServer(cfg *config.Config) (*Server, error) {
	pages, err := loadPageRoutes()
	if err != nil {
		return nil, err
	}
	if cfg.WebDistDir != "" {
		return newExternalServer(cfg.WebDistDir, pages)
	}
	return newServerWithPages(embeddedFiles, distRoot, pages), nil
}

// newExternalServer serves an operator-provided dist directory. An invalid
// directory fails startup instead of silently falling back to the embedded UI.
// index.html is read per request on purpose: the extra stat+read on every
// page hit is the accepted cost of replacing dist files without a restart.
// Do not cache it without revisiting that contract.
func newExternalServer(dir string, pages []pageRoute) (*Server, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("WEB_DIST_DIR: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("WEB_DIST_DIR %q is not a directory", dir)
	}
	files := os.DirFS(dir)
	if _, err := fs.Stat(files, "index.html"); err != nil {
		return nil, fmt.Errorf("WEB_DIST_DIR %q does not contain index.html: %w", dir, err)
	}
	return &Server{
		files: files,
		root:  ".",
		pages: clonePages(pages),
	}, nil
}

// clonePages defensively copies route descriptors so later mutations by the
// caller cannot leak into an already-built server.
func clonePages(pages []pageRoute) []pageRoute {
	return append([]pageRoute(nil), pages...)
}

func newServer(files fs.FS, root string) *Server {
	pages, err := loadPageRoutes()
	if err != nil {
		panic(err)
	}
	return newServerWithPages(files, root, pages)
}

func newServerWithPages(files fs.FS, root string, pages []pageRoute) *Server {
	index, err := fs.ReadFile(files, path.Join(root, "index.html"))
	if err != nil {
		index = []byte(fallbackIndex)
	}

	return &Server{
		files: files,
		root:  root,
		index: index,
		pages: clonePages(pages),
	}
}

func acceptsHTML(value string) bool {
	for _, candidate := range strings.Split(value, ",") {
		mediaType, parameters, err := mime.ParseMediaType(strings.TrimSpace(candidate))
		if err != nil || (mediaType != "text/html" && mediaType != "application/xhtml+xml") {
			continue
		}
		quality := 1.0
		if rawQuality, ok := parameters["q"]; ok {
			quality, err = strconv.ParseFloat(rawQuality, 64)
		}
		if err == nil && quality > 0 && quality <= 1 {
			return true
		}
	}
	return false
}

func (s *Server) serveIndex(c *gin.Context) {
	s.serveIndexWithStatus(c, http.StatusOK)
}

func (s *Server) serveNotFoundIndex(c *gin.Context) {
	s.serveIndexWithStatus(c, http.StatusNotFound)
}

func (s *Server) serveIndexWithStatus(c *gin.Context, status int) {
	index := s.index
	if index == nil {
		// Lazy (external) mode only: constructors for embedded servers always
		// populate index. A read failure here means the dist directory broke at
		// runtime; fail loudly instead of silently serving the compile-time
		// fallback, matching the documented no-silent-fallback contract.
		content, err := fs.ReadFile(s.files, path.Join(s.root, "index.html"))
		if err != nil {
			c.Header("Cache-Control", "no-cache")
			c.Header("Content-Security-Policy", "default-src 'none'")
			c.Header("X-Content-Type-Options", "nosniff")
			c.String(http.StatusInternalServerError, "index.html unreadable: %v", err)
			return
		}
		index = content
	}

	c.Header("Cache-Control", "no-cache")
	c.Header("Content-Security-Policy", indexCSP)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Frame-Options", "DENY")
	c.Data(status, "text/html; charset=utf-8", index)
}

func (s *Server) serveThemeBootstrap(c *gin.Context) {
	content, err := fs.ReadFile(s.files, path.Join(s.root, "theme-bootstrap.js"))
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}

	c.Header("Cache-Control", "no-cache")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, "text/javascript; charset=utf-8", content)
}

func (s *Server) serveFavicon(c *gin.Context) {
	content, err := fs.ReadFile(s.files, path.Join(s.root, "favicon.svg"))
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}

	c.Header("Cache-Control", "no-cache")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, "image/svg+xml", content)
}

func (s *Server) serveAsset(c *gin.Context) {
	assetPath := path.Clean(strings.TrimPrefix(c.Param("filepath"), "/"))
	if assetPath == "." || assetPath == "" || strings.HasPrefix(assetPath, "../") {
		c.Status(http.StatusNotFound)
		return
	}

	content, err := fs.ReadFile(s.files, path.Join(s.root, "assets", assetPath))
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}

	contentType := mime.TypeByExtension(path.Ext(assetPath))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, contentType, content)
}
