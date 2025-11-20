package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
)

const oneapiTokenPath = "/api/user/get-by-token"

var defaultHTTPClient = &http.Client{Timeout: 10 * time.Second}

// validateOneAPIToken ensures the provided OneAPI token is active by calling the billing service.
func (s *Telegram) validateOneAPIToken(ctx context.Context, key string) (string, error) {
	sanitized := strings.TrimSpace(key)
	if sanitized == "" {
		return "", errors.New("oneapi api key is empty")
	}

	logger := gmw.GetLogger(ctx)
	if logger != nil {
		logger.Debug("validating oneapi token", zap.String("token_mask", maskToken(sanitized)))
	}

	endpoint := strings.TrimSuffix(oneapi.BillingAPI, "/") + oneapiTokenPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", errors.Wrap(err, "build oneapi validation request")
	}
	req.Header.Set("Authorization", "Bearer "+sanitized)

	client := defaultHTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", errors.Wrap(err, "call oneapi validation api")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", errors.Wrap(err, "read oneapi validation response")
	}

	if resp.StatusCode != http.StatusOK {
		return "", errors.Errorf("oneapi validation http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		Success     bool   `json:"success"`
		Message     string `json:"message"`
		TokenStatus int    `json:"token_status"`
		Data        struct {
			Token struct {
				Status int `json:"status"`
			} `json:"token"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return "", errors.Wrap(err, "decode oneapi validation response")
	}

	tokenActive := payload.TokenStatus == 1 || payload.Data.Token.Status == 1
	if !payload.Success || !tokenActive {
		msg := payload.Message
		if msg == "" {
			msg = "token inactive"
		}
		return "", errors.Errorf("oneapi validation failed: %s", msg)
	}

	if logger != nil {
		logger.Debug("oneapi token validated", zap.String("token_mask", maskToken(sanitized)))
	}

	return sanitized, nil
}
