package oneapi

import (
	"strconv"
	"strings"
)

// quotaUnitsPerUSD is shared by charging and display conversion.
const quotaUnitsPerUSD = 500000

// ToolPrice describes the configured per-call charge, not a payment receipt.
// Amount is a decimal string so JSON and browser rendering cannot round it.
type ToolPrice struct {
	Currency string `json:"currency"`
	Unit     string `json:"unit"`
	Amount   string `json:"amount"`
}

// SharedToolPrices snapshots the same prices used by both external interfaces.
// Missing/negative entries must be shown as unknown, never inferred to be free.
// It does not check eligibility, issue a charge, or assert a billing outcome.
func SharedToolPrices() map[string]ToolPrice {
	prices := map[string]Price{
		"web_search": PriceWebSearch, "web_fetch": PriceWebFetch,
		"extract_key_info": PriceExtractKeyInfo, "find_tool": PriceFindTool,
	}
	result := make(map[string]ToolPrice, len(prices))
	for name, price := range prices {
		if price < 0 {
			continue
		}
		result[name] = ToolPrice{Currency: "USD", Unit: "call", Amount: priceDecimal(price)}
	}
	return result
}

// priceDecimal formats non-negative quota units exactly without floating point.
func priceDecimal(price Price) string {
	units := int64(price)
	whole, remainder := units/quotaUnitsPerUSD, units%quotaUnitsPerUSD
	if remainder == 0 {
		return strconv.FormatInt(whole, 10)
	}
	// Each quota unit is exactly two microdollars; this product is below 1e6.
	fraction := strconv.FormatInt(1000000+remainder*(1000000/quotaUnitsPerUSD), 10)[1:]
	return strconv.FormatInt(whole, 10) + "." + strings.TrimRight(fraction, "0")
}
