package oneapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	gutils "github.com/Laisky/go-utils/v6"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/library"
	"github.com/Laisky/laisky-blog-graphql/library/httpclient"
)

// BillingAPI is the external billing api endpoint
var BillingAPI = "https://oneapi.laisky.com"

// consumeClient returns the client for consume calls, which carry the
// caller's one-api key. It is http.DefaultClient as configured at call time
// (its Transport and Timeout keep applying), except that it never follows
// redirects, like the API key validator: the key and the charge are only ever
// sent to BillingAPI, and a redirect answer is classified like any other
// unexpected status.
func consumeClient() *http.Client {
	return httpclient.WithoutRedirects(http.DefaultClient)
}

// Price how many quotes for 1 usd
type Price int

// Int return int value
func (p Price) Int() int {
	return int(p)
}

// USD converts a dollar amount to the existing rounded-up billing quota units.
func USD(num float64) Price {
	return Price(math.Ceil(num * quotaUnitsPerUSD))
}

var (
	// PriceUploadFileEachMB is the price for uploading each MB file
	//
	// https://ar-fees.arweave.dev/
	PriceUploadFileEachMB = USD(0.02)
	// PriceUploadFileMinimal is the minimal price for uploading a file
	PriceUploadFileMinimal = USD(0.003)
	// PriceWebSearch is the price for each web search request
	//
	// https://developers.google.com/custom-search/v1/overview#pricing
	PriceWebSearch = USD(0.005)
	// PriceWebFetch is the price for each web fetch with dynamic rendering
	PriceWebFetch = USD(0.0001)
	// PriceExtractKeyInfo is the price for each extract_key_info invocation
	PriceExtractKeyInfo = USD(0.002)
	// PriceFindTool is the price for each find_tool invocation
	PriceFindTool = USD(0.002)
)

// CheckUserExternalBilling charges cost to apikey at BillingAPI through
// one-api's /api/token/consume, which also checks the user's quota. It returns
// nil once one-api answered 200, and a *BillingError describing the outcome
// otherwise.
func CheckUserExternalBilling(ctx context.Context,
	apikey string, cost Price, costReason string) error {
	_, err := ConsumeAt(ctx, BillingAPI, apikey, cost, costReason)
	return err
}

// ConsumeReceipt describes what one-api's answer to a consume call proves
// beyond the billing outcome.
type ConsumeReceipt struct {
	// TokenAccepted reports a 200 application/json answer whose envelope says
	// "success": true. one-api serves /api/token/consume behind TokenAuth, so
	// only such an answer proves that one-api accepted the key when the call
	// ran; a 200 HTML page (a proxy or maintenance page) or a non-success
	// envelope proves nothing, as for API key validation.
	TokenAccepted bool
}

// maxConsumeReceiptBytes bounds how much of a 200 consume answer is read to
// recognize one-api's success envelope.
const maxConsumeReceiptBytes = 64 << 10

// ConsumeAt is CheckUserExternalBilling against the one-api origin baseURL,
// also reporting what the answer proves about the key. The billing outcome
// and error are exactly CheckUserExternalBilling's: any 200 counts as an
// accepted charge.
func ConsumeAt(ctx context.Context, baseURL, apikey string, cost Price, costReason string) (receipt ConsumeReceipt, err error) {
	logger := gmw.GetLogger(ctx)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// push cost to remote billing
	var reqBody bytes.Buffer
	if err = json.NewEncoder(&reqBody).Encode(
		map[string]any{
			"phase":          "single",
			"add_used_quota": cost,
			"add_reason":     costReason,
		}); err != nil {
		// Nothing left this process, so nothing can have been charged.
		return receipt, errors.Wrap(&BillingError{Outcome: BillingNotAttempted,
			Reason: "encode consume request"}, "marshal request body")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(baseURL, "/")+"/api/token/consume", &reqBody)
	if err != nil {
		return receipt, errors.Wrap(&BillingError{Outcome: BillingNotAttempted,
			Reason: "build consume request"}, "push cost to external billing api")
	}
	if token := library.StripBearerPrefix(apikey); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")

	// A consume is never refused locally (the tool call already ran), but it
	// shares one-api's per-IP REST limit, so the API key gate must see it.
	DefaultCallLedger().Record(time.Now(), RouteTokenConsume)
	resp, err := consumeClient().Do(req) //nolint: bodyclose
	if err != nil {
		// A transport failure or timeout cannot distinguish "never delivered"
		// from "applied but the response was lost", so the outcome is
		// undetermined. Never infer that nothing was charged.
		return receipt, errors.Wrap(&BillingError{Outcome: BillingUnknown,
			Reason: "consume request did not complete"}, "do request")
	}
	defer gutils.LogErr(resp.Body.Close, logger)

	if resp.StatusCode != http.StatusOK {
		outcome := billingOutcomeForStatus(resp.StatusCode)
		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBillingErrorBodyBytes))
		if readErr != nil {
			// The status already classified the outcome; an unreadable body
			// must not downgrade a denial into an unknown state.
			return receipt, errors.Wrap(&BillingError{Outcome: outcome, Status: resp.StatusCode,
				Reason: "consume rejected; response body unreadable"}, "read body")
		}
		return receipt, errors.WithStack(&BillingError{Outcome: outcome, Status: resp.StatusCode,
			Reason: boundedBillingReason(string(respBody))})
	}
	receipt.TokenAccepted = isOneAPISuccessEnvelope(resp.Header.Get("Content-Type"), resp.Body)
	logger.Info("push cost to external billing api success",
		zap.Int("cost", cost.Int()), zap.Bool("token_accepted", receipt.TokenAccepted))
	return receipt, nil
}

// isOneAPISuccessEnvelope reports whether a bounded body is one-api's JSON
// envelope with "success": true, the same proof API key validation requires.
func isOneAPISuccessEnvelope(contentType string, body io.Reader) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "application/json" {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(body, maxConsumeReceiptBytes+1))
	if err != nil || len(data) > maxConsumeReceiptBytes {
		return false
	}
	var envelope struct {
		Success bool `json:"success"`
	}
	return json.Unmarshal(data, &envelope) == nil && envelope.Success
}

// maxBillingErrorBodyBytes bounds how much of a remote error body is retained,
// so a hostile or verbose billing service cannot inflate logs and audit rows.
const maxBillingErrorBodyBytes = 512

// boundedBillingReason normalizes a remote error body into a short reason.
func boundedBillingReason(body string) string {
	reason := strings.TrimSpace(body)
	if reason == "" {
		return "consume rejected without a reason"
	}
	if len(reason) > 200 {
		reason = reason[:200]
	}
	return reason
}
