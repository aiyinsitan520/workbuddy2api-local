package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/scheduler"
	gatewayserver "workbuddy2api/internal/server"
	"workbuddy2api/internal/upstream"
)

// Local management adapter. Shares the live account pool and scheduler.
func withWorkbench(next http.Handler, sch *scheduler.Scheduler, p *pool.Pool, up *upstream.Client, key, authDir string, shutdown func()) http.Handler {
	var mu sync.Mutex
	state := map[string]any{"state": "idle"}
	respond := func(w http.ResponseWriter, code int, value any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(value)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) < 9 || r.URL.Path[:9] != "/_manage/" {
			next.ServeHTTP(w, r)
			return
		}
		if key == "" || r.Header.Get("Authorization") != "Bearer "+key {
			respond(w, 401, map[string]string{"error": "invalid API key"})
			return
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/_manage/daily-chat":
			uid := r.Header.Get("X-Workbench-Account-UID")
			a := p.AuthByUID(uid)
			if a == nil || !a.IsGlobal() {
				respond(w, 400, map[string]string{"error": "invalid global task account"})
				return
			}
			r.URL.Path = "/v1/chat/completions"
			next.ServeHTTP(w, gatewayserver.WithAutomationRequest(r))
		case r.Method == "GET" && r.URL.Path == "/_manage/model-pricing":
			uid := r.URL.Query().Get("uid")
			a := p.AuthByUID(uid)
			if a == nil || !a.IsGlobal() {
				respond(w, 400, map[string]string{"error": "invalid global account"})
				return
			}
			models, err := up.FetchGlobalPricing(a)
			if err != nil {
				respond(w, 502, map[string]string{"error": "上游模型倍率查询失败"})
				return
			}
			for i := range models {
				if cost, ok := p.ModelCost(uid, models[i].ID); ok {
					models[i].ObservedPer1k = &cost
				}
			}
			respond(w, 200, map[string]any{"models": models, "ts": time.Now().Unix()})
		case r.Method == "GET" && r.URL.Path == "/_manage/task":
			mu.Lock()
			defer mu.Unlock()
			respond(w, 200, state)
		case r.Method == "POST" && r.URL.Path == "/_manage/task":
			var request struct {
				Name string `json:"name"`
			}
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&request) != nil {
				respond(w, 400, map[string]string{"error": "invalid request"})
				return
			}
			// expiry = 手动跑一次 refresh token 到期巡检（只读 + 弹提醒，不打上游）。
			actions := map[string]func(){"checkin": sch.RunCheckinNow, "travel": sch.RunTravelNow, "activity": sch.RunActivityNow, "keepalive": sch.RunKeepaliveNow, "school": sch.RunSchoolNow, "cat": sch.RunCatNow, "expiry": sch.RunExpiryAlertNow}
			action, ok := actions[request.Name]
			if !ok && request.Name != "credits" {
				respond(w, 400, map[string]string{"error": "unknown task"})
				return
			}
			if request.Name == "checkin" || request.Name == "travel" || request.Name == "school" || request.Name == "cat" {
				cn, _, _, _, _ := p.CountsDetailedForRealm("cn")
				if cn == 0 {
					respond(w, 409, map[string]string{"error": "该任务需要国内版账号；国际版账号不支持国内任务中心。"})
					return
				}
			}
			mu.Lock()
			if state["state"] == "running" {
				mu.Unlock()
				respond(w, 409, map[string]string{"error": "已有任务正在执行，请稍后再试。"})
				return
			}
			state = map[string]any{"name": request.Name, "state": "running", "started_at": time.Now().Unix(), "id": fmt.Sprint(time.Now().UnixNano())}
			mu.Unlock()
			go func() {
				var results any
				var failure string
				defer func() {
					if v := recover(); v != nil {
						failure = fmt.Sprint(v)
					}
					mu.Lock()
					defer mu.Unlock()
					state["state"] = "completed"
					state["finished_at"] = time.Now().Unix()
					state["results"] = results
					if failure != "" {
						state["state"] = "failed"
						state["error"] = failure
					}
					log.Printf("workbench task %s: %s", request.Name, state["state"])
					p.Flush()
				}()
				if request.Name == "credits" {
					accounts := []*auth.Auth{}
					for _, st := range p.List() {
						// A routing exclusion must not hide read-only account balances.
						if a := p.AuthByUID(st.UID); a != nil {
							accounts = append(accounts, a)
						}
					}
					mu.Lock()
					state["total"] = len(accounts)
					state["done"] = 0
					state["results"] = []map[string]any{}
					mu.Unlock()
					results = collectCredits(accounts, up.ResourceSummary, func(rows []map[string]any) {
						mu.Lock()
						state["results"] = rows
						state["done"] = len(rows)
						mu.Unlock()
					}, func(uid string, remain int64) { p.SetCredits(uid, remain) })
				} else {
					action()
				}
			}()
			respond(w, 202, map[string]string{"state": "running", "name": request.Name})
		case r.Method == "POST" && r.URL.Path == "/_manage/account":
			var request struct {
				UID     string `json:"uid"`
				Enabled *bool  `json:"enabled"`
			}
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&request) != nil || request.Enabled == nil {
				respond(w, 400, map[string]string{"error": "invalid request"})
				return
			}
			if _, ok := p.Status(request.UID); !ok {
				respond(w, 404, map[string]string{"error": "account not found"})
				return
			}
			p.SetRoutingPaused(request.UID, !*request.Enabled)
			if *request.Enabled {
				// 重新启用同时复活被硬禁用的账号。11140 封禁与 12153 session 死亡都只能靠
				// 重新登录恢复，而重登只换凭证（pool.upsertLocked 保留 disabled/credits/冷却），
				// 若不在开关这里清掉 disabled，新 token 会永远躺在禁用态里不被选中。
				// 复活**不清**冷却与熔断：冷却由 ReenableIfCredits 在下次余额刷新时按实际余额
				// 解除（余额 > 0 才解），熔断按自身指数退避到期——两者都是账号健康度的独立
				// 证据，不该被一个路由开关抹掉。
				p.ReviveDisabled(request.UID)
			}
			p.Flush()
			respond(w, 200, map[string]bool{"ok": true})
		case r.Method == "POST" && r.URL.Path == "/_manage/reload":
			accounts, err := auth.LoadDir(authDir)
			if err != nil {
				respond(w, 500, map[string]string{"error": err.Error()})
				return
			}
			p.SyncToDir(accounts)
			p.Flush()
			respond(w, 200, map[string]bool{"ok": true})
		case r.Method == "POST" && r.URL.Path == "/_manage/shutdown":
			mu.Lock()
			busy := state["state"] == "running"
			mu.Unlock()
			if busy {
				respond(w, 409, map[string]string{"error": "任务运行中，请稍后重启。"})
				return
			}
			respond(w, 200, map[string]bool{"ok": true})
			go func() { time.Sleep(100 * time.Millisecond); shutdown() }()
		default:
			respond(w, 404, map[string]string{"error": "not found"})
		}
	})
}
