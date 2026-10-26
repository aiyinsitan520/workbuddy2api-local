package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"workbuddy2api/internal/auth"
)

func TestAutomationRouteKeepsOrdinaryExclusion(t *testing.T) {
	auth.SetGlobalEnabled(true)
	for _, tc := range []struct {
		name                 string
		automation, disabled bool
		status, calls        int
	}{
		{"ordinary_excluded", false, false, 503, 0},
		{"automated_task_allowed", true, false, 200, 1},
		{"invalid_credential_still_blocked", true, true, 503, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testPoolWith(&auth.Auth{UID: "g", AccessToken: "a", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"})
			p.SetRoutingPaused("g", true)
			if tc.disabled {
				p.Disable("g", "session dead")
			}
			calls := 0
			up := newFakeUpstream(t, func(string) (int, string, bool) { calls++; return 200, sseOK, true })
			up.GlobalEnabled = true
			h := NewHandler(Config{Pool: p, Upstream: up, APIKey: "test", GlobalEnabled: true})
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"global:fast-model","messages":[{"role":"user","content":"Summarize the supplied project notes"}],"stream":false}`))
			r.Header.Set("Authorization", "Bearer test")
			r.Header.Set("X-Workbench-Account-UID", "g")
			r.Header.Set("X-Workbench-Automation", "true")
			if tc.automation {
				r = WithAutomationRequest(r)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != tc.status || calls != tc.calls {
				t.Fatalf("status %d calls %d body %s", rec.Code, calls, rec.Body)
			}
		})
	}
}
