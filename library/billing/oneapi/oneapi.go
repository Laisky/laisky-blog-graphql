package oneapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	gutils "github.com/Laisky/go-utils/v6"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/library"
)

// BillingAPI is the external billing api endpoint
var BillingAPI = "https://oneapi.laisky.com"

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

// checkUserExternalBilling save and check billing for text-to-image models
//
// # Steps
//  1. get user's current quota from external billing api
//  2. check if user has enough quota
//  3. update user's quota
func CheckUserExternalBilling(ctx context.Context,
	apikey string, cost Price, costReason string) (err error) {
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
		return errors.Wrap(&BillingError{Outcome: BillingNotAttempted,
			Reason: "encode consume request"}, "marshal request body")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		BillingAPI+"/api/token/consume", &reqBody)
	if err != nil {
		return errors.Wrap(&BillingError{Outcome: BillingNotAttempted,
			Reason: "build consume request"}, "push cost to external billing api")
	}
	if token := library.StripBearerPrefix(apikey); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req) //nolint: bodyclose
	if err != nil {
		// A transport failure or timeout cannot distinguish "never delivered"
		// from "applied but the response was lost", so the outcome is
		// undetermined. Never infer that nothing was charged.
		return errors.Wrap(&BillingError{Outcome: BillingUnknown,
			Reason: "consume request did not complete"}, "do request")
	}
	defer gutils.LogErr(resp.Body.Close, logger)

	if resp.StatusCode != http.StatusOK {
		outcome := billingOutcomeForStatus(resp.StatusCode)
		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBillingErrorBodyBytes))
		if readErr != nil {
			// The status already classified the outcome; an unreadable body
			// must not downgrade a denial into an unknown state.
			return errors.Wrap(&BillingError{Outcome: outcome, Status: resp.StatusCode,
				Reason: "consume rejected; response body unreadable"}, "read body")
		}
		return errors.WithStack(&BillingError{Outcome: outcome, Status: resp.StatusCode,
			Reason: boundedBillingReason(string(respBody))})
	}
	logger.Info("push cost to external billing api success",
		zap.Int("cost", cost.Int()))
	return nil
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
