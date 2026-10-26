package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"workbuddy2api/internal/auth"
)

func TestTargetAccountIsolation(t *testing.T) {
	auth.SetGlobalEnabled(true)
	for _, tc := range []struct {
		name, uid, key        string
		failure, disabled     bool
		wantStatus, wantCalls int
	}{
		{"selected", "g2", "test", false, false, 200, 1},
		{"failure_no_rotation", "g2", "test", true, false, 503, 1},
		{"disabled_no_fallback", "g2", "test", false, true, 503, 0},
		{"unknown", "missing", "test", false, false, 400, 0},
		{"realm_mismatch", "cn", "test", false, false, 400, 0},
		{"unauthenticated", "g2", "wrong", false, false, 401, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			p := testPoolWith(&auth.Auth{UID: "g1", AccessToken: "at1", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"}, &auth.Auth{UID: "g2", AccessToken: "at2", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"}, &auth.Auth{UID: "cn", AccessToken: "cn", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"})
			if tc.disabled {
				p.Disable("g2", "test")
			}
			up := newFakeUpstream(t, func(a string) (int, string, bool) {
				calls++
				if a != "Bearer at2" {
					t.Errorf("wrong account: %s", a)
				}
				if tc.failure {
					return 500, `{"error":"upstream failure"}`, false
				}
				return 200, sseOK, true
			})
			up.GlobalEnabled = true
			h := NewHandler(Config{Pool: p, Upstream: up, APIKey: "test", GlobalEnabled: true})
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"global:fast-model","messages":[{"role":"user","content":"Summarize supplied project notes"}],"stream":false}`))
			r.Header.Set("Authorization", "Bearer "+tc.key)
			r.Header.Set("X-Workbench-Account-UID", tc.uid)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d want %d: %s", rec.Code, tc.wantStatus, rec.Body)
			}
			if calls != tc.wantCalls {
				t.Fatalf("calls=%d want=%d", calls, tc.wantCalls)
			}
		})
	}
}
