package main

import (
	"sync"
	"time"
	"workbuddy2api/internal/auth"
)

type resourceQuery func(*auth.Auth) (int64, int64, int64, int, error)

// Query with four workers; one upstream failure does not block other accounts.
// Published slices and rows are never mutated after publication.
func collectCredits(accounts []*auth.Auth, query resourceQuery, publish func([]map[string]any), success func(string, int64)) []map[string]any {
	jobs := make(chan *auth.Auth)
	completed := make(chan map[string]any, 4)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for a := range jobs {
				start := time.Now()
				remain, used, size, packs, err := query(a)
				row := map[string]any{"uid": a.UID, "realm": a.Realm(), "ok": err == nil, "ts": time.Now().Unix(), "elapsed_ms": time.Since(start).Milliseconds()}
				if err != nil {
					row["error"] = err.Error()
				} else {
					success(a.UID, remain)
					row["remain"], row["used"], row["size"], row["packages"] = remain, used, size, packs
				}
				completed <- row
			}
		}()
	}
	go func() {
		for _, a := range accounts {
			jobs <- a
		}
		close(jobs)
		wg.Wait()
		close(completed)
	}()
	rows := []map[string]any{}
	for row := range completed {
		rows = append(append([]map[string]any{}, rows...), row)
		publish(rows)
	}
	return rows
}
