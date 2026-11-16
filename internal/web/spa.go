package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
)

const (
	frontendDistEnvKey     = "WEB_FRONTEND_DIST_DIR"
	frontendBasePathEnvKey = "WEB_FRONTEND_BASE_PATH"

	// immutableAssetCacheControl lets browsers reuse fingerprinted bundles across visits
	// and deploys without revalidating them. The entry document stays no-store, so a new
	// deploy is picked up through the new chunk names it references.
	immutableAssetCacheControl = "public, max-age=31536000, immutable"
)

// fingerprintedAssetName matches the file names web/vite.config.ts gives build output:
// "assets/[name]-[hash:8][extname]", where the 8-character hash is Rollup's base64url
// content hash. Only such a name is safe to cache as immutable, because a new build
// that changes the bytes also changes the name. The directory alone is not enough: a
// stable-named file could still land under assets/, and it must keep revalidating.
// Keep this pattern and the Vite output names in sync.
var fingerprintedAssetName = regexp.MustCompile(`^assets/(?:[^/]+/)*[^/]*[^/-]-[A-Za-z0-9_-]{8}\.[A-Za-z0-9]+$`)

type spaHandler struct {
	root   string
	index  []byte
	base   string
	logger logSDK.Logger
	// resourceMetadataURL is advertised by agent API probes; empty uses
	// mcpauth.DefaultResourceMetadataURL.
	resourceMetadataURL string
}

// newFrontendSPAHandler serves the built frontend. publicOrigin is the
// configured MCP public origin (mcpauth.LoadPublicOrigin); empty keeps the
// default. Agent API probes advertise its RFC 9728 metadata URL.
func newFrontendSPAHandler(logger logSDK.Logger, defaultBase, publicOrigin string) http.Handler {
	if logger == nil {
		return nil
	}

	if devURL := os.Getenv("VITE_DEV_URL"); devURL != "" {
		target, err := url.Parse(devURL)
		if err != nil {
			logger.Error("parse VITE_DEV_URL", zap.Error(err), zap.String("url", devURL))
		} else {
			logger.Info("frontend dev proxy enabled", zap.String("target", devURL))
			proxy := httputil.NewSingleHostReverseProxy(target) //nolint:gosec // G704: VITE_DEV_URL is a trusted env variable set by developers
			base := normalizeBasePath(defaultBase)
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if base != "" && strings.HasPrefix(r.URL.Path, base) {
					r.URL.Path = strings.TrimPrefix(r.URL.Path, base)
					if !strings.HasPrefix(r.URL.Path, "/") {
						r.URL.Path = "/" + r.URL.Path
					}
				}
				proxy.ServeHTTP(w, r)
			})
		}
	}

	distDir := locateFrontendDist(logger)
	if distDir == "" {
		return nil
	}

	indexPath := filepath.Join(distDir, "index.html")
	indexBytes, err := readFrontendIndex(distDir)
	if err != nil {
		logger.Warn("read frontend index", zap.Error(err), zap.String("path", indexPath))
		return nil
	}

	basePath := resolveFrontendBasePath(indexBytes, defaultBase)
	if basePath != "" {
		logger.Info("frontend base path detected", zap.String("base_path", basePath))
	}
	if publicOrigin == "" {
		publicOrigin = mcpauth.DefaultPublicOrigin
	}
	warnOnProtectedResourceMismatch(logger, distDir, publicOrigin)

	return &spaHandler{
		root:                distDir,
		index:               indexBytes,
		base:                basePath,
		logger:              logger,
		resourceMetadataURL: mcpauth.ResourceMetadataURL(publicOrigin),
	}
}

// warnOnProtectedResourceMismatch warns when the shipped RFC 9728 document
// names another resource than the configured public origin: §3.3 requires
// clients to discard such metadata, so challenges would lead nowhere.
func warnOnProtectedResourceMismatch(logger logSDK.Logger, root, publicOrigin string) {
	file, err := os.OpenInRoot(root, filepath.Join(".well-known", "oauth-protected-resource"))
	if err != nil {
		logger.Debug("no protected-resource metadata document in the frontend build", zap.Error(err))
		return
	}
	defer func() { _ = file.Close() }()
	var document struct {
		Resource string `json:"resource"`
	}
	if err := json.NewDecoder(io.LimitReader(file, 64<<10)).Decode(&document); err != nil {
		logger.Warn("protected-resource metadata document is not valid JSON", zap.Error(err))
		return
	}
	if strings.TrimSuffix(document.Resource, "/") != publicOrigin {
		logger.Warn("protected-resource metadata names another resource than settings.mcp.public_origin; "+
			"update web/public/.well-known/oauth-protected-resource or the setting",
			zap.String("document_resource", document.Resource), zap.String("public_origin", publicOrigin))
	}
}

// metadataURL returns the RFC 9728 metadata URL advertised by agent probes.
func (h *spaHandler) metadataURL() string {
	if h.resourceMetadataURL == "" {
		return mcpauth.DefaultResourceMetadataURL
	}
	return h.resourceMetadataURL
}

// readFrontendIndex prevents the cached entry document from escaping the asset root.
func readFrontendIndex(root string) (content []byte, retErr error) {
	file, err := os.OpenInRoot(root, "index.html")
	if err != nil {
		return nil, errors.Wrap(err, "open frontend index within root")
	}
	defer func() {
		if err := file.Close(); err != nil {
			retErr = errors.Join(retErr, errors.Wrap(err, "close frontend index"))
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Wrap(err, "stat frontend index")
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("frontend index must be a regular file")
	}
	content, err = io.ReadAll(file)
	if err != nil {
		return nil, errors.Wrap(err, "read frontend index")
	}
	return content, nil
}

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	requestPath := h.normalizeRequestPath(r.URL.Path)
	if requestPath == "" || requestPath == "/" {
		if isAgentMode(r) && h.serveMarkdownFile(w, r, "index.md") {
			return
		}
		if prefersMarkdown(r) && h.serveMarkdownFile(w, r, "index.md") {
			return
		}
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

	if isAgentAPIProbePath(clean) {
		serveAgentAPIProbe(w, r, h.metadataURL())
		return
	}

	if clean == ".well-known/mcp" && h.serveJSONFile(w, r, filepath.Join(".well-known", "mcp.json")) {
		return
	}

	if h.serveAsset(w, r, clean, staticContentType(clean), false) {
		return
	}

	// if the path looks like a static asset (contains an extension) and we did not find
	// it, return 404 to mirror standard static hosting behavior.
	if strings.Contains(clean, ".") {
		h.logger.Debug("frontend asset not found", zap.String("path", requestPath))
		http.NotFound(w, r)
		return
	}

	h.serveIndex(w, r)
}

func (h *spaHandler) serveIndex(w http.ResponseWriter, r *http.Request) {
	setAgentDiscoveryHeaders(w)
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

// serveMarkdownFile serves a root-confined Markdown asset when it exists.
func (h *spaHandler) serveMarkdownFile(w http.ResponseWriter, r *http.Request, clean string) bool {
	return h.serveAsset(w, r, clean, "text/markdown; charset=utf-8", true)
}

// serveJSONFile serves a root-confined discovery document when it exists.
func (h *spaHandler) serveJSONFile(w http.ResponseWriter, r *http.Request, clean string) bool {
	return h.serveAsset(w, r, clean, "application/json; charset=utf-8", false)
}

// serveAsset opens within the distribution root and serves that exact handle.
// OpenInRoot rejects symlink escapes as well as traversal during concurrent renames.
func (h *spaHandler) serveAsset(w http.ResponseWriter, r *http.Request, name, contentType string, discovery bool) bool {
	file, err := os.OpenInRoot(h.root, name)
	if err != nil {
		return false
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			h.logger.Warn("close frontend asset", zap.Error(closeErr))
		}
	}()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if discovery {
		setAgentDiscoveryHeaders(w)
		w.Header().Add("Vary", "Accept")
	}
	if contentType == "" && strings.HasSuffix(name, ".md") {
		contentType = "text/markdown; charset=utf-8"
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if isFingerprintedBuildAsset(name) {
		w.Header().Set("Cache-Control", immutableAssetCacheControl)
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
	return true
}

// isFingerprintedBuildAsset reports whether a root-relative path names a content-hashed
// build output that can be cached as immutable; see fingerprintedAssetName.
func isFingerprintedBuildAsset(clean string) bool {
	return fingerprintedAssetName.MatchString(clean)
}

func staticContentType(clean string) string {
	switch clean {
	case ".well-known/api-catalog":
		return `application/linkset+json; profile="https://www.rfc-editor.org/info/rfc9727"; charset=utf-8`
	case ".well-known/http-message-signatures-directory",
		".well-known/oauth-authorization-server",
		".well-known/oauth-protected-resource",
		".well-known/webmcp":
		return "application/json; charset=utf-8"
	default:
		return ""
	}
}

func isAgentMode(r *http.Request) bool {
	if r == nil {
		return false
	}

	return strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("mode")), "agent")
}

func isAgentAPIProbePath(clean string) bool {
	switch clean {
	case "api", "api/v1", "v1", "v2", "agent/auth":
		return true
	default:
		return false
	}
}

// agentAPIProbeChallenge is the JSON body of an agent API probe answer.
type agentAPIProbeChallenge struct {
	Error            string `json:"error"`
	Description      string `json:"error_description"`
	ResourceMetadata string `json:"resource_metadata"`
}

// newAgentAPIProbeChallenge builds the probe answer for metadataURL.
func newAgentAPIProbeChallenge(metadataURL string) agentAPIProbeChallenge {
	return agentAPIProbeChallenge{
		Error:            "authentication_required",
		Description:      "Use a Laisky MCP API key in the Authorization header.",
		ResourceMetadata: metadataURL,
	}
}

// serveAgentAPIProbe answers an agent capability probe with a Bearer
// challenge pointing to the RFC 9728 metadata document at metadataURL.
func serveAgentAPIProbe(w http.ResponseWriter, r *http.Request, metadataURL string) {
	w.Header().Set("Allow", "GET, HEAD, OPTIONS, POST")
	w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+metadataURL+`"`)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(newAgentAPIProbeChallenge(metadataURL)) //nolint:errchkjson // best-effort error response
}

func prefersMarkdown(r *http.Request) bool {
	if r == nil {
		return false
	}

	accept := strings.ToLower(r.Header.Get("Accept"))
	return strings.Contains(accept, "text/markdown")
}

func setAgentDiscoveryHeaders(w http.ResponseWriter) {
	w.Header().Add("Link", `</sitemap.xml>; rel="sitemap"; type="application/xml"`)
	w.Header().Add("Link", `</index.md>; rel="alternate"; type="text/markdown"`)
	w.Header().Add("Link", `</llms.txt>; rel="alternate"; type="text/plain"`)
	w.Header().Add("Link", `</openapi.json>; rel="service-desc"; type="application/vnd.oai.openapi+json"`)
	w.Header().Add("Link", `</.well-known/api-catalog>; rel="service-desc"; type="application/json"`)
	w.Header().Add("Link", `</.well-known/mcp>; rel="mcp"; type="application/json"`)
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
		info, err := os.Stat(candidate) //nolint:gosec // G703: candidate is derived from trusted config paths
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

func resolveFrontendBasePath(index []byte, defaultBase string) string {
	if len(index) == 0 {
		return defaultFrontendBasePath(defaultBase)
	}

	if fromEnv := normalizeBasePath(os.Getenv(frontendBasePathEnvKey)); fromEnv != "" {
		return fromEnv
	}

	if detected := detectBaseHref(index); detected != "" {
		return detected
	}

	return defaultFrontendBasePath(defaultBase)
}

func defaultFrontendBasePath(defaultBase string) string {
	return normalizeBasePath(defaultBase)
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
