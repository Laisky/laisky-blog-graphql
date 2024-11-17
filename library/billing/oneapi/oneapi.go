package oneapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	gmw "github.com/Laisky/gin-middlewares/v5"
	gutils "github.com/Laisky/go-utils/v4"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"
)

const BillingAPI = "https://oneapi.laisky.com"

// Price how many quotes for 1 usd
type Price int

// Int return int value
func (p Price) Int() int {
	return int(p)
}

// USD100 return how many usd in cents
func (p Price) USDCents() int {
	return p.Int() / 5000
}

const (
	// PriceUploadFileEachMB is the price for uploading each MB file
	//
	// https://ar-fees.arweave.dev/
	PriceUploadFileEachMB  int = 0.02 * 500000
	PriceUploadFileMinimal int = 0.003 * 500000
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
			"add_used_quota": cost,
			"add_reason":     costReason,
		}); err != nil {
		return errors.Wrap(err, "marshal request body")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		BillingAPI+"/api/token/consume", &reqBody)
	if err != nil {
		return errors.Wrap(err, "push cost to external billing api")
	}
	req.Header.Add("Authorization", apikey)

	resp, err := http.DefaultClient.Do(req) //nolint: bodyclose
	if err != nil {
		return errors.Wrap(err, "do request")
	}
	defer gutils.LogErr(resp.Body.Close, logger)

	if resp.StatusCode != http.StatusOK {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return errors.Wrap(err, "read body")
		}

		return errors.Errorf("push cost to external billing api failed [%d]%s",
			resp.StatusCode, string(respBody))
	}
	logger.Info("push cost to external billing api success",
		zap.Int("cost", cost.Int()))
	return nil
}