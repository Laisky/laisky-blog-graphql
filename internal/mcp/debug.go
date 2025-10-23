package mcp

import (
	"encoding/json"
	"net/http"
	"strings"

	logSDK "github.com/Laisky/go-utils/v5/log"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// inspectorPageTemplate renders a minimal MCP Inspector frontend that targets the
// local MCP endpoint unless overridden via the `endpoint` query parameter.
const inspectorPageTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>MCP Inspector</title>
<style>
    :root { color-scheme: light dark; }
    body, html { margin: 0; padding: 0; height: 100%; font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; background-color: #0b1120; color: #d9e3f0; }
    #app { height: 100%; }
    header { position: fixed; top: 12px; left: 12px; z-index: 20; padding: 10px 14px; border-radius: 10px; background: rgba(11, 17, 32, 0.88); box-shadow: 0 8px 24px rgba(0, 0, 0, 0.35); backdrop-filter: blur(6px); font-size: 14px; line-height: 1.4; }
    header strong { display: block; font-size: 15px; margin-bottom: 6px; color: #f8fafc; }
    header code { font-family: ui-monospace, SFMono-Regular, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace; color: #5cc9f5; }
    header a { color: #5cc9f5; text-decoration: none; }
    header a:hover { text-decoration: underline; }
</style>
</head>
<body>
<header>
  <strong>MCP Inspector</strong>
  <div>Endpoint: <code id="endpoint-display"></code></div>
  <div>Override via <code>?endpoint=https://remote.example/mcp</code></div>
</header>
<div id="app"></div>
<script type="module">
const DEFAULT_ENDPOINT_PATH = __DEFAULT_ENDPOINT_PATH__;
const params = new URLSearchParams(window.location.search);
const endpointParam = params.get("endpoint");
const endpointUrl = endpointParam ? endpointParam : new URL(DEFAULT_ENDPOINT_PATH, window.location.origin).toString();
const endpointDisplay = document.getElementById("endpoint-display");
if (endpointDisplay) {
  endpointDisplay.textContent = endpointUrl;
}
const authorization = params.get("token") || params.get("authorization") || "";
(async () => {
  try {
    const module = await import("https://unpkg.com/@modelcontextprotocol/inspector-web@latest/dist/index.js");
    const createInspector = module.createInspector || module.default;
    if (typeof createInspector !== "function") {
      throw new Error("createInspector export not found");
    }
    const inspector = await createInspector({
      target: document.getElementById("app"),
      endpointUrl,
    });
    if (authorization && inspector && typeof inspector.setAuthorizationToken === "function") {
      inspector.setAuthorizationToken(authorization);
    }
    if (inspector && typeof inspector.setEndpointUrl === "function") {
      inspector.setEndpointUrl(endpointUrl);
    }
  } catch (err) {
    console.error("Failed to bootstrap MCP Inspector:", err);
    const app = document.getElementById("app");
    if (app) {
      app.innerHTML = '<main style="display:flex;align-items:center;justify-content:center;height:100%;text-align:center;padding:24px;"><div><h1 style="margin-bottom:16px;">MCP Inspector failed to load</h1><p style="margin-bottom:8px;">Check the browser console for details.</p><p style="font-size:14px;">You can also open <a href="https://inspector.modelcontextprotocol.io" target="_blank" rel="noreferrer" style="color:#5cc9f5;">inspector.modelcontextprotocol.io</a> and point it to this endpoint manually.</p></div></main>';
    }
  }
})();
</script>
</body>
</html>`

// NewInspectorHandler returns a HTTP handler that serves the MCP Inspector page.
// The handler renders a lightweight frontend that connects to the provided MCP
// endpoint path by default, while allowing callers to override it via query
// parameters.
func NewInspectorHandler(defaultEndpointPath string, logger logSDK.Logger) http.Handler {
	if defaultEndpointPath == "" {
		defaultEndpointPath = "/mcp"
	}
	if logger == nil {
		logger = log.Logger
	}

	defaultPathJSON, err := json.Marshal(defaultEndpointPath)
	if err != nil {
		logger.Warn("marshal inspector default path", zap.Error(err))
		defaultPathJSON = []byte("\"/mcp\"")
	}

	page := []byte(strings.ReplaceAll(inspectorPageTemplate, "__DEFAULT_ENDPOINT_PATH__", string(defaultPathJSON)))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if _, err := w.Write(page); err != nil {
			logger.Warn("write inspector page", zap.Error(err))
		}
	})
}
