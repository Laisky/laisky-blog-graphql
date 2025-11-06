package calllog

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	logSDK "github.com/Laisky/go-utils/v5/log"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
)

// NewHTTPHandler builds an HTTP handler exposing the call log APIs.
func NewHTTPHandler(service *Service, logger logSDK.Logger) http.Handler {
	return &httpHandler{service: service, logger: logger}
}

type httpHandler struct {
	service *Service
	logger  logSDK.Logger
}

func (h *httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/logs" && r.Method == http.MethodGet:
		h.handleList(w, r)
	default:
		h.notFound(w, r)
	}
}

func (h *httpHandler) handleList(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		h.writeError(w, http.StatusServiceUnavailable, "call log service unavailable")
		return
	}

	authCtx, err := askuser.ParseAuthorizationContext(r.Header.Get("Authorization"))
	if err != nil {
		h.writeError(w, http.StatusUnauthorized, err.Error())
		return
	}

	q := r.URL.Query()
	page := parseIntDefault(q.Get("page"), 1)
	pageSize := parseIntDefault(q.Get("page_size"), 20)
	sortField := q.Get("sort_by")
	sortOrder := q.Get("sort_order")
	tool := q.Get("tool")
	userPrefix := q.Get("user")

	from, _ := parseDateParam(q.Get("from"))
	to, hasTime := parseDateParam(q.Get("to"))
	if !to.IsZero() {
		if !hasTime {
			to = to.AddDate(0, 0, 1)
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	result, err := h.service.List(ctx, ListOptions{
		Page:       page,
		PageSize:   pageSize,
		ToolName:   tool,
		UserPrefix: userPrefix,
		APIKeyHash: authCtx.APIKeyHash,
		SortField:  sortField,
		SortOrder:  sortOrder,
		From:       from,
		To:         to,
	})
	if err != nil {
		h.log().Error("list call logs", zap.Error(err))
		h.writeError(w, http.StatusInternalServerError, "failed to list call logs")
		return
	}

	entries := make([]map[string]any, 0, len(result.Entries))
	usdDenominator := float64(oneapi.USD(1).Int())
	for _, entry := range result.Entries {
		costUSD := 0.0
		if usdDenominator > 0 {
			costUSD = float64(entry.Cost) / usdDenominator
		}

		entries = append(entries, map[string]any{
			"id":           entry.ID.String(),
			"tool":         entry.ToolName,
			"status":       entry.Status,
			"user_prefix":  entry.KeyPrefix,
			"cost_credits": entry.Cost,
			"cost_unit":    entry.CostUnit,
			"cost_usd":     formatUSD(costUSD),
			"duration_ms":  entry.DurationMillis,
			"parameters":   entry.Parameters,
			"error":        entry.ErrorMessage,
			"occurred_at":  entry.OccurredAt,
			"created_at":   entry.CreatedAt,
			"updated_at":   entry.UpdatedAt,
		})
	}

	totalPages := 0
	if pageSize > 0 {
		totalPages = int(math.Ceil(float64(result.Total) / float64(pageSize)))
	}

	response := map[string]any{
		"data": entries,
		"pagination": map[string]any{
			"page":        page,
			"page_size":   pageSize,
			"total_items": result.Total,
			"total_pages": totalPages,
			"has_next":    page < totalPages,
			"has_prev":    page > 1 && totalPages > 0,
		},
		"sort": map[string]any{
			"field": sortField,
			"order": strings.ToUpper(sortOrder),
		},
		"filters": map[string]any{
			"tool":         tool,
			"user":         userPrefix,
			"from":         from,
			"to_exclusive": to,
		},
		"meta": map[string]any{
			"quotes_per_usd": oneapi.USD(1).Int(),
		},
	}

	h.writeJSON(w, response)
}

func (h *httpHandler) notFound(w http.ResponseWriter, r *http.Request) {
	h.writeError(w, http.StatusNotFound, "resource not found")
}

func (h *httpHandler) writeError(w http.ResponseWriter, status int, message string) {
	if status >= 500 {
		h.log().Error("call log http error", zap.Int("status", status), zap.String("message", message))
	} else {
		h.log().Warn("call log http warning", zap.Int("status", status), zap.String("message", message))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": message})
}

func (h *httpHandler) writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload)
}

func (h *httpHandler) log() logSDK.Logger {
	if h.logger != nil {
		return h.logger
	}
	return logSDK.Shared.Named("call_log_http")
}

func parseIntDefault(value string, def int) int {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return def
	}
	num, err := strconv.Atoi(trimmed)
	if err != nil {
		return def
	}
	return num
}

func parseDateParam(value string) (time.Time, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}, false
	}

	if ts, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return ts.UTC(), true
	}

	const dateLayout = "2006-01-02"
	if ts, err := time.ParseInLocation(dateLayout, trimmed, time.UTC); err == nil {
		return ts, false
	}

	return time.Time{}, false
}

func formatUSD(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "0.0000"
	}
	return fmt.Sprintf("%.4f", value)
}
