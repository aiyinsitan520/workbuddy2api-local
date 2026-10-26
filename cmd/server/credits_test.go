package main

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
	"workbuddy2api/internal/auth"
)

func TestCreditsBoundedAndIncremental(t *testing.T) {
	accounts := []*auth.Auth{}
	for i := 0; i < 9; i++ {
		accounts = append(accounts, &auth.Auth{UID: fmt.Sprint(i)})
	}
	slow := make(chan struct{})
	defer close(slow)
	var active, peak, successes atomic.Int32
	progress := make(chan []map[string]any, 9)
	finished := make(chan []map[string]any, 1)
	go func() {
		finished <- collectCredits(accounts, func(a *auth.Auth) (int64, int64, int64, int, error) {
			count := active.Add(1)
			for old := peak.Load(); count > old; old = peak.Load() {
				if peak.CompareAndSwap(old, count) {
					break
				}
			}
			defer active.Add(-1)
			if a.UID == "0" {
				<-slow
			}
			time.Sleep(10 * time.Millisecond)
			if a.UID == "2" {
				return 0, 0, 0, 0, errors.New("upstream 500")
			}
			return 90, 10, 100, 1, nil
		}, func(rows []map[string]any) { progress <- rows }, func(string, int64) { successes.Add(1) })
	}()
	var first []map[string]any
	for i := 0; i < 8; i++ {
		select {
		case rows := <-progress:
			if first == nil {
				first = rows
			}
			if len(rows) != i+1 {
				t.Fatalf("incremental result size %d", len(rows))
			}
		case <-time.After(time.Second):
			t.Fatal("fast accounts waited for slow account")
		}
	}
	if peak.Load() != 4 {
		t.Fatalf("worker peak %d, want 4", peak.Load())
	}
	if len(first) != 1 {
		t.Fatal("previous published slice mutated")
	}
	slow <- struct{}{}
	select {
	case rows := <-finished:
		if len(rows) != 9 || successes.Load() != 8 {
			t.Fatalf("rows %d successes %d", len(rows), successes.Load())
		}
		failed := 0
		for _, row := range rows {
			if row["ok"] == false {
				failed++
			}
		}
		if failed != 1 {
			t.Fatalf("failed %d", failed)
		}
	case <-time.After(time.Second):
		t.Fatal("collection did not finish")
	}
}
