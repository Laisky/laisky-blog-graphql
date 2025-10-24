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

const frontendDistEnvKey = "WEB_FRONTEND_DIST_DIR"

type spaHandler struct {
	root   string
	index  []byte
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

	return &spaHandler{
		root:   distDir,
		index:  indexBytes,
		logger: logger,
	}
}

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	requestPath := r.URL.Path
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
