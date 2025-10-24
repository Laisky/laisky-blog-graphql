package web

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	logSDK "github.com/Laisky/go-utils/v5/log"
	"github.com/Laisky/zap"
)

const (
	frontendDistEnvKey     = "WEB_FRONTEND_DIST_DIR"
	frontendBasePathEnvKey = "WEB_FRONTEND_BASE_PATH"
)

type spaHandler struct {
	root   string
	index  []byte
	base   string
	logger logSDK.Logger
}

func newFrontendSPAHandler(logger logSDK.Logger) http.Handler {
	if logger == nil {
		return nil
	}

	distDir := locateFrontendDist(logger)
	if distDir == "" {
		return nil
	}

	indexPath := filepath.Join(distDir, "index.html")
	indexBytes, err := os.ReadFile(indexPath)
	if err != nil {
		logger.Warn("read frontend index", zap.Error(err), zap.String("path", indexPath))
		return nil
	}

	basePath := resolveFrontendBasePath(indexBytes)
	if basePath != "" {
		logger.Info("frontend base path detected", zap.String("base_path", basePath))
	}

	return &spaHandler{
		root:   distDir,
		index:  indexBytes,
		base:   basePath,
		logger: logger,
	}
}

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	requestPath := h.normalizeRequestPath(r.URL.Path)
	if requestPath == "" || requestPath == "/" {
		h.serveIndex(w, r)
		return
	}

	clean := filepath.Clean(requestPath)
	clean = strings.TrimPrefix(clean, "/")
	if clean != "" && strings.Contains(clean, "..") {
		h.logger.Warn("reject potential path traversal", zap.String("path", requestPath))
		http.NotFound(w, r)
		return
	}

	if clean == "" {
		h.serveIndex(w, r)
		return
	}

	fsPath := filepath.Join(h.root, clean)
	info, err := os.Stat(fsPath)
	if err == nil && !info.IsDir() {
		http.ServeFile(w, r, fsPath)
		return
	}

	// if the path looks like a static asset (contains an extension) and we did not find
	// it, return 404 to mirror standard static hosting behaviour.
	if strings.Contains(clean, ".") {
		h.logger.Debug("frontend asset not found", zap.String("path", requestPath))
		http.NotFound(w, r)
		return
	}

	h.serveIndex(w, r)
}

func (h *spaHandler) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write(h.index); err != nil {
		h.logger.Warn("write frontend index", zap.Error(err))
	}
}

func locateFrontendDist(logger logSDK.Logger) string {
	var candidates []string

	if override := strings.TrimSpace(os.Getenv(frontendDistEnvKey)); override != "" {
		candidates = append(candidates, override)
	}

	if exePath, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exePath)
		candidates = append(candidates,
			filepath.Join(exeDir, "web", "dist"),
			filepath.Join(exeDir, "dist"),
		)
	}

	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, "web", "dist"),
		)
	}

	if _, file, _, ok := runtime.Caller(0); ok {
		sourceDir := filepath.Dir(file)
		candidates = append(candidates,
			filepath.Join(sourceDir, "../../web/dist"),
		)
	}

	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		info, err := os.Stat(candidate)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				logger.Debug("inspect frontend dist", zap.Error(err), zap.String("path", candidate))
			}
			continue
		}
		if info.IsDir() {
			logger.Info("frontend assets located", zap.String("path", candidate))
			return candidate
		}
	}

	logger.Warn("frontend assets not found", zap.String("env", frontendDistEnvKey))
	return ""
}

func resolveFrontendBasePath(index []byte) string {
	if len(index) == 0 {
		return defaultFrontendBasePath()
	}

	if fromEnv := normalizeBasePath(os.Getenv(frontendBasePathEnvKey)); fromEnv != "" {
		return fromEnv
	}

	if detected := detectBaseHref(index); detected != "" {
		return detected
	}

	return defaultFrontendBasePath()
}

func defaultFrontendBasePath() string {
	return normalizeBasePath("/mcp")
}

func normalizeBasePath(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed == "/" {
		return ""
	}
	if !strings.HasPrefix(trimmed, "/") {
		trimmed = "/" + trimmed
	}
	trimmed = strings.TrimRight(trimmed, "/")
	if trimmed == "" || trimmed == "/" {
		return ""
	}
	return trimmed
}

func detectBaseHref(index []byte) string {
	lower := strings.ToLower(string(index))
	start := strings.Index(lower, "<base")
	if start == -1 {
		return ""
	}
	end := strings.Index(lower[start:], ">")
	if end == -1 {
		return ""
	}
	segment := lower[start : start+end]
	hrefAttr := "href="
	hrefPos := strings.Index(segment, hrefAttr)
	if hrefPos == -1 {
		return ""
	}
	valueStart := hrefPos + len(hrefAttr)
	if valueStart >= len(segment) {
		return ""
	}
	quote := segment[valueStart]
	if quote != '\'' && quote != '"' {
		return ""
	}
	valueStart++
	valueEnd := strings.IndexRune(segment[valueStart:], rune(quote))
	if valueEnd == -1 {
		return ""
	}
	value := segment[valueStart : valueStart+valueEnd]
	return normalizeBasePath(value)
}

func (h *spaHandler) normalizeRequestPath(raw string) string {
	if raw == "" {
		return raw
	}

	path := raw
	if h.base != "" {
		if path == h.base {
			path = "/"
		} else if strings.HasPrefix(path, h.base+"/") {
			path = path[len(h.base):]
		}
	}

	return path
}
