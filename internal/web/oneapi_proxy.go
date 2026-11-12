package web

import (
	"net/http"
	"strconv"
	"strings"

	gmw "github.com/Laisky/gin-middlewares/v7"
	"github.com/Laisky/zap"
	"github.com/gin-gonic/gin"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
)

type oneapiBalanceRequest struct {
	APIKey string `json:"api_key"`
}

type oneapiBalanceResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type oneapiQuotaData struct {
	RemainQuota float64 `json:"remain_quota"`
	UsedQuota   float64 `json:"used_quota"`
}

// registerOneapiProxyRoutes registers REST endpoints that proxy OneAPI calls from the frontend.
func registerOneapiProxyRoutes(server *gin.Engine, prefix urlPrefixConfig) {
	if server == nil {
		return
	}

	handler := oneapiBalanceHandler()
	path := prefix.join("/api/oneapi/balance")
	server.POST(path, handler)
	server.OPTIONS(path, handler)
	if trimmed := strings.TrimSuffix(path, "/"); trimmed != path {
		server.POST(trimmed, handler)
		server.OPTIONS(trimmed, handler)
	}

	if prefix.public == "" {
		server.POST("/api/oneapi/balance", handler)
		server.OPTIONS("/api/oneapi/balance", handler)
	}
}

// oneapiBalanceHandler returns a handler that proxies OneAPI token balance requests.
// It expects JSON body {"api_key":"..."} and returns {success,data:{remain_quota,used_quota}}.
func oneapiBalanceHandler() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		logger := gmw.GetLogger(ctx).Named("oneapi_balance")

		switch ctx.Request.Method {
		case http.MethodPost:
			// continue
		case http.MethodOptions:
			ctx.Header("Allow", "POST, OPTIONS")
			ctx.AbortWithStatus(http.StatusNoContent)
			return
		default:
			ctx.AbortWithStatus(http.StatusMethodNotAllowed)
			return
		}

		var req oneapiBalanceRequest
		if err := ctx.ShouldBindJSON(&req); err != nil {
			ctx.JSON(http.StatusBadRequest, oneapiBalanceResponse{Success: false, Message: "invalid request"})
			return
		}

		apiKey := normalizeOneapiAPIKey(req.APIKey)
		if apiKey == "" {
			ctx.JSON(http.StatusBadRequest, oneapiBalanceResponse{Success: false, Message: "api_key required"})
			return
		}

		// The shared one-api gate charges this call to the caller's source and
		// the global budget, and reuses recent answers per key hash.
		balance, err := mcpauth.Installed().Balance(ctx.Request.Context(), apiKey)
		if err != nil {
			if mcpauth.IsUnavailable(err) {
				logger.Debug("oneapi balance unavailable", zap.Error(err))
				ctx.Header("Retry-After", strconv.Itoa(mcpauth.RetryAfterSeconds))
				ctx.JSON(http.StatusOK, oneapiBalanceResponse{Success: false, Message: "balance temporarily unavailable; retry shortly"})
				return
			}
			logger.Debug("oneapi balance refused", zap.Error(err))
			ctx.JSON(http.StatusOK, oneapiBalanceResponse{Success: false, Message: "failed to fetch balance"})
			return
		}

		ctx.JSON(http.StatusOK, oneapiBalanceResponse{Success: true, Data: &oneapiQuotaData{RemainQuota: balance.RemainQuota, UsedQuota: balance.UsedQuota}})
	}
}

// normalizeOneapiAPIKey strips optional "Bearer " prefix and trims whitespace.
func normalizeOneapiAPIKey(value string) string {
	out := strings.TrimSpace(value)
	for {
		lower := strings.ToLower(out)
		if !strings.HasPrefix(lower, "bearer ") {
			break
		}
		out = strings.TrimSpace(out[len("bearer "):])
	}
	return out
}
