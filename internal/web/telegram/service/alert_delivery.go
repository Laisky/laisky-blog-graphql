package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
)

const (
	// MaxAlertPayloadBytes bounds the complete formatted alert, including its type header.
	// This is a local admission budget, below Telegram's published document limit.
	MaxAlertPayloadBytes  = 2 * 1024 * 1024
	maxAlertResponseBytes = 64 * 1024
	maxAlertTextUnits     = 4096
	defaultAlertTextUnits = 3000
)

var (
	// ErrAlertPayloadTooLarge rejects the complete payload without trimming any bytes.
	ErrAlertPayloadTooLarge = errors.New("complete telegram alert exceeds byte limit")
	// ErrAlertInvalidPayload rejects empty or invalid UTF-8 formatted alert text.
	ErrAlertInvalidPayload = errors.New("telegram alert requires nonempty valid UTF-8 text")
)

// AlertDeliveryError describes a rejected or unconfirmed attempt without secret-bearing errors.
// Unconfirmed means a caller retry might duplicate an accepted message. Numeric API
// metadata may be used by the caller, but this transport never retries automatically.
type AlertDeliveryError struct {
	HTTPStatus        int
	APIErrorCode      int
	RetryAfterSeconds int
	Unconfirmed       bool
	cause             error
}

// Error returns bounded transport metadata, excluding request URLs, payloads and API descriptions.
func (e *AlertDeliveryError) Error() string {
	outcome := "telegram alert rejected"
	if e.Unconfirmed {
		outcome = "telegram alert outcome unconfirmed"
	}
	return fmt.Sprintf("%s (HTTP %d, API %d, retry_after %d seconds)", outcome, e.HTTPStatus, e.APIErrorCode, e.RetryAfterSeconds)
}

// Unwrap exposes only a safe context cancellation/deadline cause, never a raw HTTP error.
func (e *AlertDeliveryError) Unwrap() error { return e.cause }

// ValidateAlertPayload rejects the entire formatted payload before network activity.
func ValidateAlertPayload(msg string) error {
	if len(msg) > MaxAlertPayloadBytes {
		return errors.WithStack(ErrAlertPayloadTooLarge)
	}
	if msg == "" || !utf8.ValidString(msg) {
		return errors.WithStack(ErrAlertInvalidPayload)
	}
	return nil
}

// alertTextThreshold retains max_len as a routing threshold, never a truncation budget.
// UTF-16 units conservatively bound the documented character limit without claiming
// Telegram defines its character limit in Go runes or UTF-16 units.
func alertTextThreshold() int {
	threshold := gconfig.Shared.GetInt("settings.telegram.max_len")
	if threshold <= 0 || threshold > maxAlertTextUnits {
		return defaultAlertTextUnits
	}
	return threshold
}

// alertUsesDocument decides whether complete text exceeds the conservative text threshold.
func alertUsesDocument(msg string) bool {
	units := 0
	threshold := alertTextThreshold()
	for _, r := range msg {
		units++
		if r > 0xffff {
			units++
		}
		if units > threshold {
			return true
		}
	}
	return false
}

// SendAlertToUser makes one context-bound attempt using the existing bot and recipient.
// Short text is literal; larger text is an in-memory document containing every byte.
// Success means Telegram acknowledged the matching message/document, not that a user
// read it. Timeouts and ambiguous responses never cause automatic retries or fallback.
func (s *Telegram) SendAlertToUser(ctx context.Context, uid int, msg string) (sendErr error) {
	if err := ValidateAlertPayload(msg); err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("telegram alert context is required")
	}
	if err := ctx.Err(); err != nil {
		return errors.WithStack(&AlertDeliveryError{Unconfirmed: false, cause: err})
	}
	if s == nil || s.bot == nil || s.bot.URL == "" || s.bot.Token == "" || uid == 0 {
		return errors.WithStack(&AlertDeliveryError{})
	}
	document := alertUsesDocument(msg)
	request, err := s.alertRequest(ctx, uid, msg, document)
	if err != nil {
		return err
	}
	client := s.alertClient
	if client == nil {
		client = &http.Client{Timeout: time.Minute}
	}
	// Do not mutate the shared client or follow a bot-token URL to another destination.
	clientCopy := *client
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	response, err := clientCopy.Do(request)
	if err != nil {
		return alertUnconfirmed(ctx, 0, err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil && sendErr == nil {
			sendErr = alertUnconfirmed(ctx, response.StatusCode, nil)
		}
	}()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAlertResponseBytes+1))
	if err != nil {
		return alertUnconfirmed(ctx, response.StatusCode, err)
	}
	if len(body) > maxAlertResponseBytes {
		return alertUnconfirmed(ctx, response.StatusCode, nil)
	}
	var result alertAPIResponse
	if err = json.Unmarshal(body, &result); err != nil {
		return alertUnconfirmed(ctx, response.StatusCode, nil)
	}
	if !result.OK && result.ErrorCode != 0 {
		return errors.WithStack(&AlertDeliveryError{HTTPStatus: response.StatusCode, APIErrorCode: result.ErrorCode, RetryAfterSeconds: result.Parameters.RetryAfter})
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !result.OK || result.Result.MessageID <= 0 {
		return alertUnconfirmed(ctx, response.StatusCode, nil)
	}
	if document {
		doc := result.Result.Document
		if doc == nil || doc.FileID == "" || (doc.FileSize != nil && *doc.FileSize != len(msg)) {
			return alertUnconfirmed(ctx, response.StatusCode, nil)
		}
	} else if result.Result.Text == nil || *result.Result.Text != msg {
		return alertUnconfirmed(ctx, response.StatusCode, nil)
	}
	return nil
}

// alertAPIResponse models only safe acknowledgement fields; descriptions are never retained.
type alertAPIResponse struct {
	OK         bool `json:"ok"`
	ErrorCode  int  `json:"error_code"`
	Parameters struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
	Result struct {
		MessageID int     `json:"message_id"`
		Text      *string `json:"text"`
		Document  *struct {
			FileID   string `json:"file_id"`
			FileSize *int   `json:"file_size"`
		} `json:"document"`
	} `json:"result"`
}

// alertUnconfirmed discards raw errors that may contain the bot token or a response body.
func alertUnconfirmed(ctx context.Context, status int, err error) error {
	safe := &AlertDeliveryError{HTTPStatus: status, Unconfirmed: true}
	if ctx.Err() != nil {
		safe.cause = ctx.Err()
	} else if errors.Is(err, context.DeadlineExceeded) {
		safe.cause = context.DeadlineExceeded
	}
	return errors.WithStack(safe)
}

// alertRequest constructs one bounded in-memory request without disk or background writers.
func (s *Telegram) alertRequest(ctx context.Context, uid int, msg string, document bool) (*http.Request, error) {
	method := "sendMessage"
	var body io.Reader
	contentType := "application/json"
	var contentLength int64
	if document {
		method = "sendDocument"
		var envelope bytes.Buffer
		writer := multipart.NewWriter(&envelope)
		if err := writer.WriteField("chat_id", strconv.Itoa(uid)); err != nil {
			return nil, errors.WithStack(&AlertDeliveryError{})
		}
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="document"; filename="alert.txt"`)
		header.Set("Content-Type", "text/plain; charset=utf-8")
		if _, err := writer.CreatePart(header); err != nil {
			return nil, errors.WithStack(&AlertDeliveryError{})
		}
		headerLength := envelope.Len()
		if err := writer.Close(); err != nil {
			return nil, errors.WithStack(&AlertDeliveryError{})
		}
		wire := envelope.Bytes()
		// Reader-only wrapper prevents strings.Reader.WriteTo from materializing
		// a complete second byte slice during the HTTP upload.
		body = io.MultiReader(bytes.NewReader(wire[:headerLength]), struct{ io.Reader }{strings.NewReader(msg)}, bytes.NewReader(wire[headerLength:]))
		contentType = writer.FormDataContentType()
		contentLength = int64(len(wire) + len(msg))
	} else {
		var encoded bytes.Buffer
		encoder := json.NewEncoder(&encoded)
		encoder.SetEscapeHTML(false)
		data := struct {
			ChatID  int    `json:"chat_id"`
			Text    string `json:"text"`
			Preview struct {
				Disabled bool `json:"is_disabled"`
			} `json:"link_preview_options"`
		}{ChatID: uid, Text: msg}
		data.Preview.Disabled = true
		if err := encoder.Encode(data); err != nil {
			return nil, errors.WithStack(&AlertDeliveryError{})
		}
		contentLength = int64(encoded.Len())
		body = struct{ io.Reader }{bytes.NewReader(encoded.Bytes())}
	}
	target := strings.TrimRight(s.bot.URL, "/") + "/bot" + s.bot.Token + "/" + method
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, body)
	if err != nil {
		return nil, errors.WithStack(&AlertDeliveryError{})
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Accept", "application/json")
	request.ContentLength = contentLength
	return request, nil
}
