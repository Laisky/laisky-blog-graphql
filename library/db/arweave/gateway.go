package arweave

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"math/rand/v2"
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
	// gatewayMaxAttempts bounds the attempts for one gateway request, except
	// for a committed chunk, which is retried until its deadline.
	gatewayMaxAttempts    = 5
	gatewayRetryBaseDelay = time.Second
	gatewayRetryMaxDelay  = 30 * time.Second
	// gatewayMaxRedirects bounds the redirects a read may follow, as Go's
	// default client does.
	gatewayMaxRedirects = 10
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
		httpClient:     &http.Client{CheckRedirect: checkGatewayRedirect},
		requestTimeout: gatewayRequestTimeout,
		maxAttempts:    gatewayMaxAttempts,
		retryBaseDelay: gatewayRetryBaseDelay,
		retryMaxDelay:  gatewayRetryMaxDelay,
	}
}

// checkGatewayRedirect lets reads follow redirects but returns a redirected
// POST as it is. For a 301, 302 or 303, Go would otherwise re-send the POST
// as a GET of the Location, and a 200 there would pass for an accepted
// transaction or chunk that the gateway never stored. The redirect is
// reported instead; see gatewayClient.do.
func checkGatewayRedirect(_ *http.Request, via []*http.Request) error {
	if via[0].Method != http.MethodGet {
		return http.ErrUseLastResponse
	}
	if len(via) >= gatewayMaxRedirects {
		return errors.Errorf("stopped after %d redirects", gatewayMaxRedirects)
	}
	return nil
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
// A node answers 303, with no Location, to a chunk it stored in its disk pool
// but keeps in none of its own storage modules; ar-io-node also counts that
// as accepted.
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

// gatewayCall describes one gateway request.
type gatewayCall struct {
	op       string
	method   string
	path     []string
	payload  []byte
	classify func(status int, body string) gatewayOutcome
	// untilDeadline retries transient failures until ctx's deadline rather
	// than up to maxAttempts. It is for a chunk of a sent transaction, whose
	// reward is already committed and whose ctx carries the committed-phase
	// bound. Without a deadline, maxAttempts still applies.
	untilDeadline bool
}

// gatewayResponse is one HTTP answer from the gateway, whatever its status.
type gatewayResponse struct {
	status     int
	body       []byte
	retryAfter time.Duration
	location   string
}

// redirected reports a redirect that checkGatewayRedirect did not follow.
// A 3xx without a Location is not a redirect: a node answers 303 to a chunk
// it accepted only temporarily.
func (r gatewayResponse) redirected() bool {
	return r.status >= http.StatusMultipleChoices && r.status < http.StatusBadRequest && r.location != ""
}

// price returns the reward, in winston, the gateway quotes for size bytes.
func (g *gatewayClient) price(ctx context.Context, size int) (string, error) {
	body, err := g.do(ctx, gatewayCall{
		op: "get price", method: http.MethodGet, path: []string{"price", strconv.Itoa(size)}, classify: classifyRequest,
	})
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
	body, err := g.do(ctx, gatewayCall{
		op: "get transaction anchor", method: http.MethodGet, path: []string{"tx_anchor"}, classify: classifyRequest,
	})
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

// postTransaction sends a signed transaction header. It keeps the attempt
// limit: until the gateway accepts the header, the reward is most likely
// not committed, and a caller should hear about an outage promptly.
func (g *gatewayClient) postTransaction(ctx context.Context, tx *schema.Transaction) error {
	payload, err := json.Marshal(tx)
	if err != nil {
		return errors.Wrap(err, "marshal transaction")
	}
	if _, err = g.do(ctx, gatewayCall{
		op: "post transaction", method: http.MethodPost, path: []string{"tx"}, payload: payload, classify: classifyTransaction,
	}); err != nil {
		return errors.WithStack(err)
	}
	return nil
}

// postChunk sends one data chunk with its Merkle proof. The transaction is
// already accepted, so transient failures are retried until ctx's deadline.
func (g *gatewayClient) postChunk(ctx context.Context, chunk *schema.GetChunk) error {
	payload, err := chunk.Marshal()
	if err != nil {
		return errors.Wrap(err, "marshal chunk")
	}
	if _, err = g.do(ctx, gatewayCall{
		op: "post chunk", method: http.MethodPost, path: []string{"chunk"}, payload: payload, classify: classifyChunk,
		untilDeadline: true,
	}); err != nil {
		return errors.WithStack(err)
	}
	return nil
}

// do sends one gateway request, retrying transient failures with capped,
// jittered exponential backoff. Every attempt has its own timeout. ctx ends
// the request, including an attempt in flight, and is never retried. A
// redirect that was not followed is a permanent failure.
func (g *gatewayClient) do(ctx context.Context, call gatewayCall) ([]byte, error) {
	logger := gmw.GetLogger(ctx)
	endpoint, err := url.JoinPath(g.baseURL, call.path...)
	if err != nil {
		return nil, errors.Wrapf(err, "%s: build gateway URL", call.op)
	}
	limit := g.maxAttempts
	if _, bounded := ctx.Deadline(); call.untilDeadline && bounded {
		limit = 0
	}

	var failure error
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, stoppedRetrying(call.op, attempt-1, err, failure)
		}

		resp, err := g.attempt(ctx, call.method, endpoint, call.payload)
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return nil, stoppedRetrying(call.op, attempt, err, failure)
			}
			failure = errors.Wrapf(err, "%s", call.op)
		case resp.redirected():
			return nil, errors.WithStack(&gatewayStatusError{
				op: call.op, status: resp.status, body: "redirect to " + quoteBody([]byte(resp.location)),
			})
		default:
			statusErr := &gatewayStatusError{op: call.op, status: resp.status, body: quoteBody(resp.body)}
			switch call.classify(resp.status, string(resp.body)) {
			case gatewaySucceeded:
				return resp.body, nil
			case gatewayFailed:
				return nil, errors.WithStack(statusErr)
			case gatewayRetryable:
				failure = errors.WithStack(statusErr)
			}
		}

		if limit > 0 && attempt >= limit {
			return nil, errors.Wrapf(failure, "gave up after %d attempts", attempt)
		}
		delay := g.retryDelay(attempt, resp.retryAfter)
		logger.Warn("retry arweave gateway request",
			zap.String("op", call.op),
			zap.Int("attempt", attempt),
			zap.Duration("delay", delay),
			zap.Error(failure))
		if err := sleepContext(ctx, delay); err != nil {
			return nil, stoppedRetrying(call.op, attempt, err, failure)
		}
	}
}

// stoppedRetrying reports a request that ctx ended, keeping ctx's error in
// the chain and quoting the last failure that caused a retry, if any.
func stoppedRetrying(op string, attempts int, ctxErr, lastFailure error) error {
	if lastFailure == nil {
		return errors.Wrapf(ctxErr, "%s", op)
	}
	return errors.Wrapf(ctxErr, "%s: stopped after %d attempts, last failure: %s", op, attempts, lastFailure.Error())
}

// attempt performs one bounded HTTP exchange. A transport error is returned
// as err; any HTTP response, whatever its status, is returned as data.
func (g *gatewayClient) attempt(ctx context.Context, method, endpoint string, payload []byte) (gatewayResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, g.requestTimeout)
	defer cancel()

	var reqBody io.Reader
	if payload != nil {
		reqBody = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reqBody)
	if err != nil {
		return gatewayResponse{}, errors.Wrap(err, "build request")
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return gatewayResponse{}, errors.Wrap(err, "send request")
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGatewayResponseBytes+1))
	if err != nil {
		return gatewayResponse{}, errors.Wrap(err, "read response")
	}
	if len(body) > maxGatewayResponseBytes {
		return gatewayResponse{}, errors.Errorf("response exceeds %d bytes", maxGatewayResponseBytes)
	}
	return gatewayResponse{
		status:     resp.StatusCode,
		body:       body,
		retryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		location:   resp.Header.Get("Location"),
	}, nil
}

// retryDelay doubles from retryBaseDelay up to retryMaxDelay, then draws the
// delay from the upper half of that backoff ("equal jitter") so that uploads
// failing together do not retry in lockstep. A longer Retry-After is
// honored, still capped by retryMaxDelay.
func (g *gatewayClient) retryDelay(attempt int, retryAfter time.Duration) time.Duration {
	backoff := g.retryMaxDelay
	if shift := attempt - 1; shift < 31 {
		if d := g.retryBaseDelay << shift; d > 0 && d < backoff {
			backoff = d
		}
	}
	delay := backoff - rand.N(backoff/2+1) //nolint:gosec // jitter needs no cryptographic randomness
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
