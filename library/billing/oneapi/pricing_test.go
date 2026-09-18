package oneapi

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"
)

// TestSharedToolPrices uses the live charging variables, not a separate UI tariff.
func TestSharedToolPrices(t *testing.T) {
	prices := SharedToolPrices()
	for name, source := range map[string]Price{
		"web_search": PriceWebSearch, "web_fetch": PriceWebFetch,
		"extract_key_info": PriceExtractKeyInfo, "find_tool": PriceFindTool,
	} {
		got, ok := prices[name]
		if !ok || got.Currency != "USD" || got.Unit != "call" || got.Amount != priceDecimal(source) {
			t.Fatalf("%s did not use its configured price: %+v", name, got)
		}
		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err := json.Unmarshal(encoded, &wire); err != nil {
			t.Fatal(err)
		}
		if _, ok := wire["amount"].(string); !ok {
			t.Fatal("wire price lost decimal string type")
		}
	}
	if len(prices) != 4 {
		t.Fatal("unexpected price metadata")
	}
	delete(prices, "web_fetch")
	if _, ok := SharedToolPrices()["web_fetch"]; !ok {
		t.Fatal("caller mutated the shared tariff")
	}
}

// TestPriceDecimal covers one unit, free, whole dollars and large integer amounts.
func TestPriceDecimal(t *testing.T) {
	for _, tc := range []struct {
		units Price
		want  string
	}{
		{0, "0"}, {1, "0.000002"}, {50, "0.0001"}, {1000, "0.002"}, {2500, "0.005"},
		{500000, "1"}, {500001, "1.000002"}, {499999, "0.999998"},
	} {
		if got := priceDecimal(tc.units); got != tc.want {
			t.Fatalf("%d: %s != %s", tc.units, got, tc.want)
		}
	}
	if strconv.IntSize == 64 {
		maximum := int64(math.MaxInt64)
		if got := priceDecimal(Price(maximum)); got != "18446744073709.551614" {
			t.Fatal(got)
		}
	}
}

// TestPricingConversionScale retains the existing charging-unit conversion.
func TestPricingConversionScale(t *testing.T) {
	for _, tc := range []struct {
		usd   float64
		units Price
	}{{0, 0}, {1, 500000}, {0.002, 1000}, {0.000001, 1}} {
		if got := USD(tc.usd); got != tc.units {
			t.Fatalf("USD(%v) = %d", tc.usd, got)
		}
	}
}
