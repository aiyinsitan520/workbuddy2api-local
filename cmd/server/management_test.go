package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/scheduler"
	"workbuddy2api/internal/upstream"
)

func TestWorkbenchManagementBoundaries(t *testing.T) {
	p := pool.New("")
	defer p.Close()
	p.Add(&auth.Auth{UID: "account1"})
	up := upstream.New()
	sch := scheduler.New(scheduler.Config{Pool: p, Upstream: up})
	h := withWorkbench(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), sch, p, up, "test-secret", t.TempDir(), func() {})
	for _, tc := range []struct {
		method, path, body, key string
		want                    int
	}{
		{"GET", "/_manage/task", "", "", 401},
		{"POST", "/_manage/task", `{"name":"shell"}`, "test-secret", 400},
		{"POST", "/_manage/account", `{"uid":"account1"}`, "test-secret", 400},
		{"POST", "/_manage/account", `{"uid":"missing","enabled":false}`, "test-secret", 404},
		{"POST", "/_manage/account", `{"uid":"account1","enabled":false}`, "test-secret", 200},
		{"GET", "/healthz", "", "", 204},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.key != "" {
			req.Header.Set("Authorization", "Bearer "+tc.key)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s %s: %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}
	st, _ := p.Status("account1")
	if !st.RoutingPaused {
		t.Fatal("pause did not exclude live account from ordinary routing")
	}
	if st.Disabled {
		t.Fatal("routing pause must not flip the system disabled flag")
	}
}

// 重新启用必须能复活被硬禁用的账号：重登只换凭证，disabled 只有这里能清。
func TestWorkbenchAccountEnableRevivesDisabled(t *testing.T) {
	p := pool.New("")
	defer p.Close()
	p.Add(&auth.Auth{UID: "account1"})
	p.Disable("account1", "account banned by upstream (11140 request illegal), re-login required")
	up := upstream.New()
	sch := scheduler.New(scheduler.Config{Pool: p, Upstream: up})
	h := withWorkbench(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), sch, p, up, "test-secret", t.TempDir(), func() {})
	req := httptest.NewRequest("POST", "/_manage/account", strings.NewReader(`{"uid":"account1","enabled":true}`))
	req.Header.Set("Authorization", "Bearer test-secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("enable: %d, want 200", rec.Code)
	}
	st, _ := p.Status("account1")
	if st.Disabled || st.DisabledReason != "" {
		t.Fatalf("enable 应复活禁用账号: disabled=%v reason=%q", st.Disabled, st.DisabledReason)
	}
	if st.RoutingPaused {
		t.Fatal("enable 应清除路由排除")
	}
	if got := p.Pick(); got == nil || got.UID != "account1" {
		t.Fatalf("复活后应可被选中, got %+v", got)
	}
}
