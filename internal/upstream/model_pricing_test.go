package upstream

import (
	"testing"
	"time"
)

func TestModelPricingExpiredPromotionNotFree(t *testing.T) {
	raw := []byte(`{"code":0,"data":{"models":[{"id":"expired","credits":"x0.00"},{"id":"active","credits":"x0.52"},{"id":"cheap","credits":"x0.14 credits"},{"id":"unknown"},{"id":"off","credits":"x0","disabled":true}],"modelPromotions":[{"enabled":true,"modelIds":["expired"],"discount":{"factor":0},"schedule":{"validFrom":"2026-07-01T00:00:00Z","validUntil":"2026-09-30T00:00:00Z"}},{"enabled":true,"modelIds":["active"],"discount":{"factor":0},"schedule":{"validFrom":"2026-10-01T00:00:00Z","validUntil":"2026-11-01T00:00:00Z"}}]}}`)
	rows, err := parseModelPricing(raw, time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 || rows[0].Multiplier != nil || !rows[0].FreeUnverified || rows[1].Multiplier == nil || *rows[1].Multiplier != 0 || *rows[2].Multiplier != 0.14 || rows[3].Multiplier != nil {
		t.Fatalf("unexpected prices %+v", rows)
	}
}
