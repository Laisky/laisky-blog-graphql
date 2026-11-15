package arweave

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	"github.com/Laisky/zap"
	"github.com/permadao/goar/schema"
)

const (
	// gatewayRequestTimeout bounds one HTTP attempt, so a stalled gateway
	// cannot hold an upload even when the caller sets no deadline.
	gatewayRequestTimeout = time.Minute
	// gatewayMaxAttempts bounds the attempts for one gateway request.
	gatewayMaxAttempts    = 5
	gatewayRetryBaseDelay = time.Second
	gatewayRetryMaxDelay  = 30 * time.Second
	// maxGatewayResponseBytes caps how much of a gateway response is read.
	maxGatewayResponseBytes = 64 << 10
	// maxGatewayErrorBodyBytes caps how much of a response is quoted in errors.
	maxGatewayErrorBodyBytes = 256
)

// gatewayClient calls the Arweave gateway endpoints an upload needs.
//
// goar's Client sends every request through http.DefaultClient with no
// context or timeout, so a goar upload can neither be canceled nor bounded.
// This client keeps goar for signing and chunking and owns the transport.
type gatewayClient struct {
	baseURL        string
	httpClient     *http.Client
	requestTimeout time.Duration
	maxAttempts    int
	retryBaseDelay time.Duration
	retryMaxDelay  time.Duration
}

func newGatewayClient(baseURL string) *gatewayClient {
	return &gatewayClient{
		baseURL:        baseURL,
		httpClient:     &http.Client{},
		requestTimeout: gatewayRequestTimeout,
		maxAttempts:    gatewayMaxAttempts,
		retryBaseDelay: gatewayRetryBaseDelay,
		retryMaxDelay:  gatewayRetryMaxDelay,
	}
}

// gatewayStatusError reports a gateway response that is not a success.
type gatewayStatusError struct {
	op     string
	status int
	body   string
}

// Error implements error.
func (e *gatewayStatusError) Error() string {
	return fmt.Sprintf("%s: gateway returned %d %q", e.op, e.status, e.body)
}

// gatewayOutcome classifies a gateway response.
type gatewayOutcome int

const (
	gatewaySucceeded gatewayOutcome = iota
	gatewayRetryable
	gatewayFailed
)

// classifyRequest treats rate limits and server errors as transient.
func classifyRequest(status int, _ string) gatewayOutcome {
	switch {
	case status >= http.StatusOK && status < http.StatusMultipleChoices:
		return gatewaySucceeded
	case status == http.StatusTooManyRequests || status >= http.StatusInternalServerError:
		return gatewayRetryable
	default:
		return gatewayFailed
	}
}

// classifyTransaction also accepts the answers to a resent transaction: a
// node replies 208 for ten minutes after accepting it and 400 with these
// bodies afterwards. The ID is fixed by the signature, so both mean "sent".
func classifyTransaction(status int, body string) gatewayOutcome {
	if status == http.StatusBadRequest {
		switch strings.TrimSpace(body) {
		case "Transaction is already in the mempool.", "Transaction is already on the weave.":
			return gatewaySucceeded
		}
	}
	return classifyRequest(status, body)
}

// classifyChunk follows the Arweave uploader: a 400 is retried unless the
// node names a fatal error (data_root_not_found, for example, is transient).
// A node also answers 303 to a chunk it accepted only temporarily.
func classifyChunk(status int, body string) gatewayOutcome {
	switch status {
	case http.StatusSeeOther:
		return gatewaySucceeded
	case http.StatusBadRequest:
		if _, fatal := schema.FATAL_CHUNK_UPLOAD_ERRORS[strings.TrimSpace(body)]; fatal {
			return gatewayFailed
		}
		return gatewayRetryable
	default:
		return classifyRequest(status, body)
	}
}

// price returns the reward, in winston, the gateway quotes for size bytes.
func (g *gatewayClient) price(ctx context.Context, size int) (string, error) {
	body, err := g.do(ctx, "get price", http.MethodGet, nil, classifyRequest, "price", strconv.Itoa(size))
	if err != nil {
		return "", errors.WithStack(err)
	}
	reward, ok := new(big.Int).SetString(strings.TrimSpace(string(body)), 10)
	if !ok || reward.Sign() <= 0 {
		return "", errors.Errorf("gateway quoted an invalid reward %q", quoteBody(body))
	}
	return reward.String(), nil
}

// anchor returns a recent block hash or transaction ID to anchor a transaction to.
func (g *gatewayClient) anchor(ctx context.Context) (string, error) {
	body, err := g.do(ctx, "get transaction anchor", http.MethodGet, nil, classifyRequest, "tx_anchor")
	if err != nil {
		return "", errors.WithStack(err)
	}
	// goar's deep hash silently hashes an undecodable anchor as empty bytes,
	// which would sign a transaction the network rejects, so check it here.
	anchor := strings.TrimSpace(string(body))
	raw, err := base64.RawURLEncoding.DecodeString(anchor)
	if err != nil || (len(raw) != 32 && len(raw) != 48) {
		return "", errors.Errorf("gateway returned an invalid transaction anchor %q", quoteBody(body))
	}
	return anchor, nil
}

// postTransaction sends a signed transaction header.
func (g *gatewayClient) postTransaction(ctx context.Context, tx *schema.Transaction) error {
	payload, err := json.Marshal(tx)
	if err != nil {
		return errors.Wrap(err, "marshal transaction")
	}
	if _, err = g.do(ctx, "post transaction", http.MethodPost, payload, classifyTransaction, "tx"); err != nil {
		return errors.WithStack(err)
	}
	return nil
}

// postChunk sends one data chunk with its Merkle proof.
func (g *gatewayClient) postChunk(ctx context.Context, chunk *schema.GetChunk) error {
	payload, err := chunk.Marshal()
	if err != nil {
		return errors.Wrap(err, "marshal chunk")
	}
	if _, err = g.do(ctx, "post chunk", http.MethodPost, payload, classifyChunk, "chunk"); err != nil {
		return errors.WithStack(err)
	}
	return nil
}

// do sends one gateway request, retrying transient failures with capped
// exponential backoff. Every attempt has its own timeout; ctx ends the
// request, including an attempt in flight, and is never retried.
func (g *gatewayClient) do(ctx context.Context, op, method string, payload []byte,
	classify func(status int, body string) gatewayOutcome, pathElems ...string) ([]byte, error) {
	logger := gmw.GetLogger(ctx)
	endpoint, err := url.JoinPath(g.baseURL, pathElems...)
	if err != nil {
		return nil, errors.Wrapf(err, "%s: build gateway URL", op)
	}

	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, errors.Wrapf(err, "%s", op)
		}

		body, status, retryAfter, err := g.attempt(ctx, method, endpoint, payload)
		var failure error
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return nil, errors.Wrapf(err, "%s", op)
			}
			failure = errors.Wrapf(err, "%s", op)
		default:
			switch classify(status, string(body)) {
			case gatewaySucceeded:
				return body, nil
			case gatewayFailed:
				return nil, errors.WithStack(&gatewayStatusError{op: op, status: status, body: quoteBody(body)})
			case gatewayRetryable:
				failure = errors.WithStack(&gatewayStatusError{op: op, status: status, body: quoteBody(body)})
			}
		}

		if attempt >= g.maxAttempts {
			return nil, errors.Wrapf(failure, "gave up after %d attempts", attempt)
		}
		delay := g.retryDelay(attempt, retryAfter)
		logger.Warn("retry arweave gateway request",
			zap.String("op", op),
			zap.Int("attempt", attempt),
			zap.Duration("delay", delay),
			zap.Error(failure))
		if err := sleepContext(ctx, delay); err != nil {
			return nil, errors.Wrapf(err, "%s: wait to retry", op)
		}
	}
}

// attempt performs one bounded HTTP exchange. A transport error is returned
// as err; any HTTP response, whatever its status, is returned as data.
func (g *gatewayClient) attempt(ctx context.Context, method, endpoint string,
	payload []byte) (body []byte, status int, retryAfter time.Duration, err error) {
	ctx, cancel := context.WithTimeout(ctx, g.requestTimeout)
	defer cancel()

	var reqBody io.Reader
	if payload != nil {
		reqBody = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reqBody)
	if err != nil {
		return nil, 0, 0, errors.Wrap(err, "build request")
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, 0, 0, errors.Wrap(err, "send request")
	}
	defer func() { _ = resp.Body.Close() }()

	body, err = io.ReadAll(io.LimitReader(resp.Body, maxGatewayResponseBytes+1))
	if err != nil {
		return nil, 0, 0, errors.Wrap(err, "read response")
	}
	if len(body) > maxGatewayResponseBytes {
		return nil, 0, 0, errors.Errorf("response exceeds %d bytes", maxGatewayResponseBytes)
	}
	return body, resp.StatusCode, parseRetryAfter(resp.Header.Get("Retry-After")), nil
}

// retryDelay doubles from retryBaseDelay up to retryMaxDelay and honors a
// longer Retry-After, still capped by retryMaxDelay.
func (g *gatewayClient) retryDelay(attempt int, retryAfter time.Duration) time.Duration {
	delay := g.retryMaxDelay
	if shift := attempt - 1; shift < 31 {
		if d := g.retryBaseDelay << shift; d > 0 && d < delay {
			delay = d
		}
	}
	if retryAfter > delay {
		delay = min(retryAfter, g.retryMaxDelay)
	}
	return delay
}

// parseRetryAfter reads the delay-seconds form of Retry-After.
func parseRetryAfter(v string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// quoteBody returns at most maxGatewayErrorBodyBytes of a response for errors.
func quoteBody(body []byte) string {
	if len(body) > maxGatewayErrorBodyBytes {
		return string(body[:maxGatewayErrorBodyBytes]) + "..."
	}
	return string(body)
}
