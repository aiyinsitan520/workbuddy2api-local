package scheduler

import (
	"testing"
	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/upstream"
)

func TestRoutingPausedAccountKeepsTokenMaintenance(t *testing.T) {
	f := &fakeUpstream{}
	srv := f.server()
	defer srv.Close()
	p := pool.New("")
	a := &auth.Auth{UID: "cn", AccessToken: "old", RefreshToken: "rt", ExpiresAt: 1}
	p.Add(a)
	p.SetRoutingPaused("cn", true)
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	s := New(Config{Pool: p, Upstream: up})
	s.RunKeepaliveNow()
	if f.refreshCalls.Load() != 1 || a.AccessToken != "new" {
		t.Fatal("paused account did not refresh")
	}
	if p.Pick() != nil {
		t.Fatal("maintenance restored ordinary routing")
	}
}

func TestRoutingPausedAccountKeepsCheckin(t *testing.T) {
	f := &fakeUpstream{}
	srv := f.server()
	defer srv.Close()
	p := pool.New("")
	p.Add(&auth.Auth{UID: "cn", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.SetRoutingPaused("cn", true)
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	s := New(Config{Pool: p, Upstream: up})
	_, err := s.CheckinAll()
	if err != nil || f.checkinCalls.Load() != 1 {
		t.Fatalf("checkin err %v calls %d", err, f.checkinCalls.Load())
	}
	if p.Pick() != nil {
		t.Fatal("checkin restored ordinary routing")
	}
}
