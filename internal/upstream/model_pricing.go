package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"
	"workbuddy2api/internal/auth"
)

type ModelPricing struct {
	ID               string   `json:"id"`
	Multiplier       *float64 `json:"multiplier"`
	Advertised       string   `json:"advertised"`
	FreeUnverified   bool     `json:"free_unverified"`
	SupportedEfforts []string `json:"supported_efforts,omitempty"`
	ObservedPer1k    *float64 `json:"observed_per_1k,omitempty"`
}

var multiplierPattern = regexp.MustCompile(`(?i)^\s*(?:x|×)?\s*(\d+(?:\.\d+)?)\s*(?:x|×|credits)?\s*$`)

func parseModelPricing(raw []byte, now time.Time) ([]ModelPricing, error) {
	var env struct {
		Code int `json:"code"`
		Data struct {
			Models []struct {
				ID        string `json:"id"`
				Credits   string `json:"credits"`
				Disabled  bool   `json:"disabled"`
				Reasoning struct {
					SupportedEfforts []string `json:"supportedEfforts"`
				} `json:"reasoning"`
			} `json:"models"`
			Promotions []struct {
				ModelIDs []string `json:"modelIds"`
				Enabled  bool     `json:"enabled"`
				Schedule struct {
					From  string `json:"validFrom"`
					Until string `json:"validUntil"`
				} `json:"schedule"`
				Discount struct {
					Factor *float64 `json:"factor"`
				} `json:"discount"`
			} `json:"modelPromotions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	if env.Code != 0 || len(env.Data.Models) == 0 {
		return nil, fmt.Errorf("model pricing unavailable")
	}
	inactiveFree := map[string]bool{}
	activeFree := map[string]bool{}
	for _, p := range env.Data.Promotions {
		if p.Discount.Factor == nil || *p.Discount.Factor != 0 {
			continue
		}
		from, fromErr := time.Parse(time.RFC3339, p.Schedule.From)
		until, untilErr := time.Parse(time.RFC3339, p.Schedule.Until)
		active := p.Enabled && fromErr == nil && untilErr == nil && !now.Before(from) && now.Before(until)
		for _, id := range p.ModelIDs {
			if active {
				activeFree[id] = true
			} else {
				inactiveFree[id] = true
			}
		}
	}
	rows := []ModelPricing{}
	for _, m := range env.Data.Models {
		if m.Disabled || m.ID == "" {
			continue
		}
		row := ModelPricing{ID: m.ID, Advertised: m.Credits, SupportedEfforts: m.Reasoning.SupportedEfforts}
		if match := multiplierPattern.FindStringSubmatch(m.Credits); len(match) > 1 {
			v, err := strconv.ParseFloat(match[1], 64)
			if err == nil {
				row.Multiplier = &v
			}
		}
		if activeFree[m.ID] {
			v := 0.0
			row.Multiplier = &v
		}
		if row.Multiplier != nil && *row.Multiplier == 0 && inactiveFree[m.ID] && !activeFree[m.ID] {
			row.Multiplier = nil
			row.FreeUnverified = true
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// Per-account live catalog; its price metadata is never shared with CN or used
// as a promise that the upstream will grant an activity reward.
func (c *Client) FetchGlobalPricing(a *auth.Auth) ([]ModelPricing, error) {
	if !c.globalOn(a) {
		return nil, fmt.Errorf("global routing disabled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.chatBase(a)+globalModelsProbePaths[0], nil)
	if err != nil {
		return nil, err
	}
	c.CommonHeaders(req, a)
	req.Header.Set("Authorization", "Bearer "+a.AccessToken)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model pricing HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return parseModelPricing(raw, time.Now())
}
