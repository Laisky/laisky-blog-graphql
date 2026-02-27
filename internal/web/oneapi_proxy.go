package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	"github.com/Laisky/zap"
	"github.com/gin-gonic/gin"
)

const (
	oneapiBalanceDefaultURL = "https://oneapi.laisky.com/api/token/balance"
)

var (
	oneapiHTTPClient = &http.Client{Timeout: 20 * time.Second}
	oneapiBalanceURL = oneapiBalanceDefaultURL
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

		quota, err := fetchOneapiQuota(ctx.Request.Context(), apiKey)
		if err != nil {
			logger.Warn("fetch oneapi quota", zap.Error(err))
			ctx.JSON(http.StatusOK, oneapiBalanceResponse{Success: false, Message: "failed to fetch balance"})
			return
		}

		ctx.JSON(http.StatusOK, oneapiBalanceResponse{Success: true, Data: quota})
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

// fetchOneapiQuota calls OneAPI /api/token/balance and returns quota info.
func fetchOneapiQuota(ctx context.Context, apiKey string) (*oneapiQuotaData, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, oneapiBalanceURL, nil)
	if err != nil {
		return nil, errors.Wrap(err, "new request")
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := oneapiHTTPClient.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "request")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.Errorf("unexpected status %d", resp.StatusCode)
	}

	var result struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    struct {
			RemainQuota float64 `json:"remain_quota"`
			UsedQuota   float64 `json:"used_quota"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, errors.Wrap(err, "decode")
	}
	if !result.Success {
		return nil, errors.Errorf("oneapi error: %s", result.Message)
	}

	return &oneapiQuotaData{RemainQuota: result.Data.RemainQuota, UsedQuota: result.Data.UsedQuota}, nil
}
