package scheduler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/upstream"
)

func TestSkipGlobalActivityReports(t *testing.T) {
	auth.SetGlobalEnabled(true)
	calls := 0
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.Write([]byte(`{"code":0}`)) }))
	defer remote.Close()
	p := pool.New("")
	p.Add(&auth.Auth{UID: "g1", AccessToken: "at", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"})
	up := &upstream.Client{HTTP: remote.Client(), BillingBaseGlobal: remote.URL, GlobalEnabled: true}
	s := New(Config{Pool: p, Upstream: up, ActivityReportCount: 5, SkipGlobalActivityReports: true})
	s.RunActivityNow()
	if calls != 0 {
		t.Fatalf("synthetic global requests=%d, want zero", calls)
	}
}
