package scheduler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/upstream"
)

func TestNextFire(t *testing.T) {
	loc := time.Local
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, loc)
	next := nextFire(now, []int{9, 21})
	if next.Hour() != 21 || next.Day() != 27 {
		t.Errorf("next=%v want 21:00 same day", next)
	}
	now = time.Date(2026, 7, 27, 22, 0, 0, 0, loc)
	next = nextFire(now, []int{9, 21})
	if next.Hour() != 9 || next.Day() != 28 {
		t.Errorf("next=%v want 09:00 next day", next)
	}
	now = time.Date(2026, 7, 27, 9, 0, 0, 0, loc)
	next = nextFire(now, []int{9})
	if next.Day() != 28 {
		t.Errorf("exact match should roll to next day: %v", next)
	}
}

func TestNextFireMergesSchedules(t *testing.T) {
	now := time.Date(2026, 7, 27, 20, 0, 0, 0, time.Local)
	next := nextFire(now, []int{9, 21, 22})
	if next.Hour() != 21 {
		t.Errorf("next=%v want 21 (earliest of 21/22)", next)
	}
}

// TestNextWakeKeepaliveOnly 签到已过点时按保活整点唤醒。
func TestNextWakeKeepaliveOnly(t *testing.T) {
	s := New(Config{CheckinHours: []int{9}, KeepaliveHours: []int{22},
		TravelDisabled: true, ActivityDisabled: true})
	at, kinds := s.nextWake(time.Date(2026, 9, 11, 20, 0, 0, 0, time.Local))
	if want := time.Date(2026, 9, 11, 22, 0, 0, 0, time.Local); !at.Equal(want) {
		t.Errorf("next=%v want %v", at, want)
	}
	if len(kinds) != 1 || kinds[0] != taskKeepalive {
		t.Errorf("kinds=%v want [keepalive]", kinds)
	}
}

// TestNextWakeSameInstantFiresAll 签到与保活配到同一整点时两类任务都要执行。
func TestNextWakeSameInstantFiresAll(t *testing.T) {
	s := New(Config{
		CheckinHours:     []int{9, 22},
		TravelHours:      []int{}, // 禁用旅行时点干扰（仅测签到+保活同整点）
		ActivityHours:    []int{}, // 禁用活跃时点干扰
		KeepaliveHours:   []int{22},
		TravelDisabled:   true,
		ActivityDisabled: true,
		SchoolDisabled:   true,
		CatDisabled:      true,
	})
	at, kinds := s.nextWake(time.Date(2026, 9, 11, 21, 30, 0, 0, time.Local))
	if want := time.Date(2026, 9, 11, 22, 0, 0, 0, time.Local); !at.Equal(want) {
		t.Errorf("next=%v want %v", at, want)
	}
	if !hasKind(kinds, taskCheckin) || !hasKind(kinds, taskKeepalive) {
		t.Errorf("kinds=%v want checkin+keepalive（同一时刻两任务）", kinds)
	}

	// 22 点过后下一次是次日 09:00，且只含签到（旅行/活跃已禁用）。
	at, kinds = s.nextWake(time.Date(2026, 9, 11, 22, 30, 0, 0, time.Local))
	if want := time.Date(2026, 9, 12, 9, 0, 0, 0, time.Local); !at.Equal(want) {
		t.Errorf("next=%v want %v", at, want)
	}
	if len(kinds) != 1 || kinds[0] != taskCheckin {
		t.Errorf("kinds=%v want [checkin]", kinds)
	}
}

// TestNextWakeNothingScheduled 两类任务全空时返回零值，Run 只等退出信号。
func TestNextWakeNothingScheduled(t *testing.T) {
	s := &Scheduler{cfg: Config{}}
	at, kinds := s.nextWake(time.Now())
	if !at.IsZero() || len(kinds) != 0 {
		t.Errorf("at=%v kinds=%v want zero/nil", at, kinds)
	}
}

// TestNextWakeCheckinDisabled 显式禁用签到后，排程里不再有签到时点（保活照常）。
func TestNextWakeCheckinDisabled(t *testing.T) {
	s := New(Config{CheckinDisabled: true, CheckinHours: []int{9, 21}, KeepaliveHours: []int{22},
		TravelDisabled: true, ActivityDisabled: true})
	at, kinds := s.nextWake(time.Date(2026, 9, 11, 20, 0, 0, 0, time.Local))
	if want := time.Date(2026, 9, 11, 22, 0, 0, 0, time.Local); !at.Equal(want) {
		t.Errorf("next=%v want %v（不应再有 21 点签到）", at, want)
	}
	if len(kinds) != 1 || kinds[0] != taskKeepalive {
		t.Errorf("kinds=%v want [keepalive]", kinds)
	}
}

// TestNextWakeKeepaliveDisabled 显式禁用保活后，排程里不再有保活时点（签到照常）。
func TestNextWakeKeepaliveDisabled(t *testing.T) {
	s := New(Config{KeepaliveDisabled: true, CheckinHours: []int{9, 21}, KeepaliveHours: []int{22},
		TravelDisabled: true, ActivityDisabled: true})
	at, kinds := s.nextWake(time.Date(2026, 9, 11, 20, 0, 0, 0, time.Local))
	if want := time.Date(2026, 9, 11, 21, 0, 0, 0, time.Local); !at.Equal(want) {
		t.Errorf("next=%v want %v（不应再有 22 点保活）", at, want)
	}
	if len(kinds) != 1 || kinds[0] != taskCheckin {
		t.Errorf("kinds=%v want [checkin]", kinds)
	}
}

// TestNextWakeBothDisabledNothingScheduled 六类任务都显式禁用 → 无可唤醒时点。
func TestNextWakeBothDisabledNothingScheduled(t *testing.T) {
	s := New(Config{
		CheckinDisabled:   true,
		TravelDisabled:    true,
		ActivityDisabled:  true,
		KeepaliveDisabled: true,
		SchoolDisabled:    true,
		CatDisabled:       true,
		// 到期巡检是第六类之外新增的第七类，也必须显式禁用才算"全关"。
		ExpiryAlertDisabled: true,
		CheckinHours:      []int{9, 21},
		KeepaliveHours:    []int{22},
	})
	at, kinds := s.nextWake(time.Now())
	if !at.IsZero() || len(kinds) != 0 {
		t.Errorf("at=%v kinds=%v want zero/nil", at, kinds)
	}
}

// TestRunAllDisabledNoSpinNoCalls 六类任务全禁用：Run 不空转（只等退出信号），
// 且不能触发任何上游请求。
func TestRunAllDisabledNoSpinNoCalls(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "no upstream call expected", 404)
	}))
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	up := &upstream.Client{
		HTTP:          srv.Client(),
		ChatBaseCN:    srv.URL,
		BillingBaseCN: srv.URL,
	}
	s := New(Config{
		Pool:              p,
		Upstream:          up,
		CheckinDisabled:   true,
		TravelDisabled:    true,
		ActivityDisabled:  true,
		KeepaliveDisabled: true,
		SchoolDisabled:    true,
		CatDisabled:       true,
		// 不关到期巡检的话排程里仍有 10:00 唤醒点，"全禁用"前提不成立。
		ExpiryAlertDisabled: true,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	start := time.Now()
	s.Run(ctx) // 阻塞到 ctx 取消为止（无时点可等，不构造 timer）
	elapsed := time.Since(start)

	if calls.Load() != 0 {
		t.Errorf("upstream calls=%d want 0（六类全禁用）", calls.Load())
	}
	if elapsed < 200*time.Millisecond {
		t.Errorf("Run returned after %v, before ctx done（不应提前返回）", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Run took %v（不应空转/忙等）", elapsed)
	}
}

func hasKind(kinds []taskKind, k taskKind) bool {
	for _, v := range kinds {
		if v == k {
			return true
		}
	}
	return false
}

// fakeUpstream 同时模拟 billing 与 refresh。
type fakeUpstream struct {
	checkinCalls   atomic.Int32
	refreshCalls   atomic.Int32
	resourceRemain int64
}

func (f *fakeUpstream) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/daily-checkin"):
			f.checkinCalls.Add(1)
			w.Write([]byte(`{"code":0,"msg":"ok","data":{}}`))
		case strings.HasSuffix(r.URL.Path, "/get-user-resource"):
			w.Write([]byte(`{"code":0,"data":{"Response":{"Data":{"Accounts":[{"CycleCapacitySize":100,"CycleCapacityRemain":` +
				jsonI64(f.resourceRemain) + `,"CycleCapacityUsed":0}]}}}}`))
		case strings.HasSuffix(r.URL.Path, "/token/refresh"):
			f.refreshCalls.Add(1)
			w.Write([]byte(`{"code":0,"data":{"accessToken":"new","expiresIn":3600}}`))
		default:
			http.Error(w, "not found", 404)
		}
	}))
}

func jsonI64(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestRunCheckinReenablesCoolingAccount(t *testing.T) {
	f := &fakeUpstream{resourceRemain: 500}
	srv := f.server()
	defer srv.Close()

	p := pool.New("")
	a := &auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999}
	p.Add(a)
	p.Cooldown("u1", pool.CoolHard, time.Hour, "余额不足")

	up := &upstream.Client{
		HTTP:          srv.Client(),
		ChatBaseCN:    srv.URL,
		BillingBaseCN: srv.URL,
	}
	s := New(Config{
		Pool:           p,
		Upstream:       up,
		CheckinHours:   []int{9, 21},
		KeepaliveHours: []int{22},
	})
	s.RunCheckinNow()
	if f.checkinCalls.Load() != 1 {
		t.Errorf("checkin calls=%d", f.checkinCalls.Load())
	}
	st, _ := p.Status("u1")
	if st.Cooling {
		t.Errorf("account should be reenabled after checkin with credits: %+v", st)
	}
	if st.Credits != 500 {
		t.Errorf("credits=%d want 500", st.Credits)
	}
}

func TestRunKeepaliveRefreshesTokens(t *testing.T) {
	f := &fakeUpstream{}
	srv := f.server()
	defer srv.Close()

	p := pool.New("")
	a := &auth.Auth{UID: "u1", AccessToken: "old", RefreshToken: "rt", ExpiresAt: 1}
	p.Add(a)

	up := &upstream.Client{
		HTTP:          srv.Client(),
		ChatBaseCN:    srv.URL,
		BillingBaseCN: srv.URL,
	}
	s := New(Config{Pool: p, Upstream: up})
	s.RunKeepaliveNow()
	if f.refreshCalls.Load() != 1 {
		t.Errorf("refresh calls=%d", f.refreshCalls.Load())
	}
	if a.AccessToken != "new" {
		t.Errorf("token not updated: %s", a.AccessToken)
	}
}

func TestRunKeepaliveSessionDeadDisables(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"code":12153,"msg":"Offline user session not found"}`))
	}))
	defer srv.Close()

	p := pool.New("")
	a := &auth.Auth{UID: "u1", AccessToken: "old", RefreshToken: "rt", ExpiresAt: 1}
	p.Add(a)

	up := &upstream.Client{
		HTTP:          srv.Client(),
		ChatBaseCN:    srv.URL,
		BillingBaseCN: srv.URL,
	}
	s := New(Config{Pool: p, Upstream: up})
	// P0-1：12153 连续 N 次才禁用。前 2 次刷新失败不应杀号（误判防护）。
	s.RunKeepaliveNow()
	if st, _ := p.Status("u1"); st.Disabled {
		t.Fatalf("第 1 次 12153 不应禁用: %+v", st)
	}
	s.RunKeepaliveNow()
	if st, _ := p.Status("u1"); st.Disabled {
		t.Fatalf("第 2 次 12153 不应禁用: %+v", st)
	}
	// 第 3 次连续 12153 → 禁用。
	s.RunKeepaliveNow()
	st, _ := p.Status("u1")
	if !st.Disabled {
		t.Errorf("第 3 次连续 12153 应禁用: %+v", st)
	}
	if st.DisabledReason != "12153 session dead" {
		t.Errorf("disabled_reason=%q want 12153 session dead", st.DisabledReason)
	}
}

// TestRunKeepaliveSessionDeadResetBySuccess 两次 12153 后刷新成功 → 计数清零，
// 再来的 12153 从第 1 次重新计（不会因历史失败被继续追杀）。
func TestRunKeepaliveSessionDeadResetBySuccess(t *testing.T) {
	var fails atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fails.Add(1) == 3 { // 第 3 次（本次调度循环的第二轮）刷新成功
			w.Write([]byte(`{"code":0,"data":{"accessToken":"new","expiresIn":3600}}`))
			return
		}
		w.WriteHeader(401)
		w.Write([]byte(`{"code":12153,"msg":"Offline user session not found"}`))
	}))
	defer srv.Close()

	p := pool.New("")
	a := &auth.Auth{UID: "u1", AccessToken: "old", RefreshToken: "rt", ExpiresAt: 1}
	p.Add(a)

	up := &upstream.Client{
		HTTP:          srv.Client(),
		ChatBaseCN:    srv.URL,
		BillingBaseCN: srv.URL,
	}
	s := New(Config{Pool: p, Upstream: up})
	s.RunKeepaliveNow() // 12153 #1
	s.RunKeepaliveNow() // 12153 #2
	if st, _ := p.Status("u1"); st.Disabled {
		t.Fatalf("precondition: 前 2 次不应禁用: %+v", st)
	}
	s.RunKeepaliveNow() // 刷新成功 → 清计数
	// 接下来连续 2 次 12153：从新计数重新算，仍不应禁用（历史计数已清）。
	s.RunKeepaliveNow() // 12153 #1（新计数）
	s.RunKeepaliveNow() // 12153 #2（新计数）
	if st, _ := p.Status("u1"); st.Disabled {
		t.Fatalf("刷新成功清计数后连续 2 次 12153 不应禁用: %+v", st)
	}
	s.RunKeepaliveNow() // 12153 #3（新计数）→ 禁用
	if st, _ := p.Status("u1"); !st.Disabled {
		t.Fatalf("新计数第 3 次 12153 应禁用: %+v", st)
	}
}

func TestCheckinErrorDoesNotCrash(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`boom`))
	}))
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	up := &upstream.Client{
		HTTP:          srv.Client(),
		ChatBaseCN:    srv.URL,
		BillingBaseCN: srv.URL,
	}
	s := New(Config{Pool: p, Upstream: up})
	// 不应 panic
	s.RunCheckinNow()
	s.RunKeepaliveNow()
	_ = errors.New("unused")
}

// checkinStub 配置化的签到上游：可控制签到响应（ok/already/fail）、刷新是否失败、
// 余额返回值。各分支命中后 atomic 计数，便于并发安全断言。
type checkinStub struct {
	checkinBody    string // /daily-checkin 返回的完整 body（含 code/msg）
	checkinStatus  int    // /daily-checkin HTTP 状态码（0=200）
	refreshFail    bool   // /token/refresh 是否失败（返回 12153 session dead）
	refreshCalls   atomic.Int32
	resourceRemain int64
}

func (s *checkinStub) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/daily-checkin"):
			if s.checkinStatus != 0 {
				w.WriteHeader(s.checkinStatus)
			}
			w.Write([]byte(s.checkinBody))
		case strings.HasSuffix(r.URL.Path, "/get-user-resource"):
			w.Write([]byte(`{"code":0,"data":{"Response":{"Data":{"Accounts":[{"CycleCapacitySize":100,"CycleCapacityRemain":` +
				jsonI64(s.resourceRemain) + `,"CycleCapacityUsed":0}]}}}}`))
		case strings.HasSuffix(r.URL.Path, "/token/refresh"):
			s.refreshCalls.Add(1)
			if s.refreshFail {
				w.WriteHeader(401)
				w.Write([]byte(`{"code":12153,"msg":"Offline user session not found"}`))
				return
			}
			w.Write([]byte(`{"code":0,"data":{"accessToken":"new","expiresIn":3600}}`))
		default:
			http.Error(w, "not found", 404)
		}
	}))
}

// newCheckinS 构造 Pool+Upstream+Scheduler，账号 token 未过期（不触发预刷新）。
func newCheckinS(t *testing.T, stub *checkinStub) (*Scheduler, *pool.Pool) {
	t.Helper()
	srv := stub.server()
	t.Cleanup(srv.Close)
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	return New(Config{Pool: p, Upstream: up}), p
}

// TestCheckinAllOK 签到成功 → ok、余额回填、credits 指针有值。
func TestCheckinAllOK(t *testing.T) {
	s, p := newCheckinS(t, &checkinStub{
		checkinBody:    `{"code":0,"msg":"ok","data":{}}`,
		resourceRemain: 500,
	})
	out, err := s.CheckinAll()
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(out) != 1 || out[0].Status != CheckinOK {
		t.Fatalf("out=%+v want ok", out)
	}
	if out[0].Credits == nil || *out[0].Credits != 500 {
		t.Errorf("credits=%v want 500", out[0].Credits)
	}
	if st, _ := p.Status("u1"); st.Credits != 500 {
		t.Errorf("pool credits=%d want 500", st.Credits)
	}
}

// TestCheckinAllAlready "今天已签到"记 already、不回填 400 报文到 detail。
func TestCheckinAllAlready(t *testing.T) {
	s, _ := newCheckinS(t, &checkinStub{
		checkinBody:    `{"code":14001,"msg":"今天已签到"}`,
		resourceRemain: 300,
	})
	out, err := s.CheckinAll()
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if out[0].Status != CheckinAlready {
		t.Errorf("status=%q want already", out[0].Status)
	}
	if out[0].Detail != "" {
		t.Errorf("已签到不应回填 detail，got=%q", out[0].Detail)
	}
}

// TestCheckinAllFail 签到上游 500 → fail、detail 填报错。
func TestCheckinAllFail(t *testing.T) {
	s, _ := newCheckinS(t, &checkinStub{
		checkinBody:    `boom`,
		checkinStatus:  500,
		resourceRemain: 300,
	})
	out, _ := s.CheckinAll()
	if out[0].Status != CheckinFail {
		t.Errorf("status=%q want fail", out[0].Status)
	}
	if out[0].Detail == "" {
		t.Error("fail 应填 detail")
	}
}

// TestCheckinAllSkipsDisabled 禁用账号记 skipped，不参与签到。
func TestCheckinAllSkipsDisabled(t *testing.T) {
	stub := &checkinStub{checkinBody: `{"code":0,"msg":"ok","data":{}}`, resourceRemain: 100}
	srv := stub.server()
	defer srv.Close()
	p := pool.New("")
	p.Add(&auth.Auth{UID: "dis", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "ok", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Disable("dis", "test")
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	s := New(Config{Pool: p, Upstream: up})
	out, _ := s.CheckinAll()
	m := map[string]CheckinStatus{}
	for _, o := range out {
		m[o.UID] = o.Status
	}
	if m["dis"] != CheckinSkipped {
		t.Errorf("dis=%q want skipped", m["dis"])
	}
	if m["ok"] != CheckinOK {
		t.Errorf("ok=%q want ok", m["ok"])
	}
}

// TestCheckinAllSkipsNoCredentials 无 refreshToken 的账号记 skipped(no credentials)。
func TestCheckinAllSkipsNoCredentials(t *testing.T) {
	stub := &checkinStub{checkinBody: `{"code":0,"msg":"ok","data":{}}`, resourceRemain: 100}
	srv := stub.server()
	defer srv.Close()
	p := pool.New("")
	p.Add(&auth.Auth{UID: "notoken", AccessToken: "", RefreshToken: "", ExpiresAt: 9999999999})
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	s := New(Config{Pool: p, Upstream: up})
	out, _ := s.CheckinAll()
	if out[0].Status != CheckinSkipped {
		t.Errorf("status=%q want skipped", out[0].Status)
	}
	if out[0].Detail != "no credentials" {
		t.Errorf("detail=%q want no credentials", out[0].Detail)
	}
}

// TestCheckinAllRefreshBeforeExpiry token 临近过期 → 签到前先刷新，刷新成功后继续签到。
func TestCheckinAllRefreshBeforeExpiry(t *testing.T) {
	stub := &checkinStub{checkinBody: `{"code":0,"msg":"ok","data":{}}`, resourceRemain: 100}
	srv := stub.server()
	defer srv.Close()
	p := pool.New("")
	// ExpiresAt 5 分钟后过期，落在 checkinRefreshSkew(10min) 窗口内 → 触发预刷新。
	a := &auth.Auth{UID: "u1", AccessToken: "old", RefreshToken: "rt",
		ExpiresAt: time.Now().Add(5 * time.Minute).Unix()}
	p.Add(a)
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	s := New(Config{Pool: p, Upstream: up})
	out, _ := s.CheckinAll()
	if stub.refreshCalls.Load() != 1 {
		t.Errorf("refresh calls=%d want 1（过期窗口应预刷新）", stub.refreshCalls.Load())
	}
	if out[0].Status != CheckinOK {
		t.Errorf("status=%q want ok（刷新成功后继续签到）", out[0].Status)
	}
	if a.AccessToken != "new" {
		t.Errorf("token 未刷新: %s", a.AccessToken)
	}
}

// TestCheckinAllRefreshFlakyContinues 刷新抖动失败但 token 未真过期 → 继续签到（不阻断）。
func TestCheckinAllRefreshFlakyContinues(t *testing.T) {
	stub := &checkinStub{
		checkinBody:    `{"code":0,"msg":"ok","data":{}}`,
		refreshFail:    true,
		resourceRemain: 100,
	}
	srv := stub.server()
	defer srv.Close()
	p := pool.New("")
	// token 5 分钟后过期（在窗口内 → 尝试刷新），但 NeedsRefresh(0) 仍 false（未真过期）。
	// 刷新返回 12153 但不是真 session dead 的终态——token 仍有效，继续签到。
	a := &auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt",
		ExpiresAt: time.Now().Add(5 * time.Minute).Unix()}
	p.Add(a)
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	s := New(Config{Pool: p, Upstream: up})
	out, _ := s.CheckinAll()
	if out[0].Status != CheckinOK {
		t.Errorf("status=%q want ok（刷新抖动不应阻断签到）", out[0].Status)
	}
	if stub.refreshCalls.Load() != 1 {
		t.Errorf("refresh calls=%d want 1", stub.refreshCalls.Load())
	}
}

// TestCheckinAllRefreshTrulyExpiredFails 刷新失败且 token 真过期 → 记 fail。
func TestCheckinAllRefreshTrulyExpiredFails(t *testing.T) {
	stub := &checkinStub{
		checkinBody: `{"code":0,"msg":"ok","data":{}}`,
		refreshFail: true,
	}
	srv := stub.server()
	defer srv.Close()
	p := pool.New("")
	// ExpiresAt 已是过去 → NeedsRefresh(0) 为 true（真过期），刷新失败即 fail。
	a := &auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1}
	p.Add(a)
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	s := New(Config{Pool: p, Upstream: up})
	out, _ := s.CheckinAll()
	if out[0].Status != CheckinFail {
		t.Errorf("status=%q want fail（真过期 + 刷新失败）", out[0].Status)
	}
	if out[0].Detail == "" || !strings.HasPrefix(out[0].Detail, "refresh:") {
		t.Errorf("detail=%q want refresh: 前缀", out[0].Detail)
	}
}

// TestCheckinAllBusy 并发第二次调用返回 ErrBusy（TryLock 串行化）。
func TestCheckinAllBusy(t *testing.T) {
	stub := &checkinStub{checkinBody: `{"code":0,"msg":"ok","data":{}}`, resourceRemain: 100}
	s, _ := newCheckinS(t, stub)
	// 手动持锁模拟一次签到正在执行，再调 CheckinAll 应得 ErrBusy。
	s.checkinMu.Lock()
	defer s.checkinMu.Unlock()
	_, err := s.CheckinAll()
	if !errors.Is(err, ErrBusy) {
		t.Errorf("err=%v want ErrBusy", err)
	}
}

// TestCheckinAllReenablesCoolingAccount 冷却账号签到成功 + 余额恢复 → 解冻。
func TestCheckinAllReenablesCoolingAccount(t *testing.T) {
	stub := &checkinStub{checkinBody: `{"code":0,"msg":"ok","data":{}}`, resourceRemain: 500}
	srv := stub.server()
	defer srv.Close()
	p := pool.New("")
	a := &auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999}
	p.Add(a)
	p.Cooldown("u1", pool.CoolHard, time.Hour, "余额不足")
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	s := New(Config{Pool: p, Upstream: up})
	out, err := s.CheckinAll()
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if out[0].Status != CheckinOK {
		t.Errorf("status=%q want ok", out[0].Status)
	}
	if st, _ := p.Status("u1"); st.Cooling {
		t.Errorf("签到 + 余额恢复应解冻: %+v", st)
	}
}

// TestRunKeepaliveBackfillsRealm 验证 keepalive refresh 成功后，SaveAtomic 落盘文件
// 自动补上 realm 标识：老 global 文件（domain=workbuddy.ai，无 realm 键）→ global，
// 老 CN 文件（空 domain，无 realm 键）→ cn；再次 refresh 不改变已补的标识（幂等）。
func TestRunKeepaliveBackfillsRealm(t *testing.T) {
	cases := []struct {
		name       string
		fixture    string
		filename   string
		wantRealm  string
	}{
		{
			name: "老 global 落盘补 global",
			fixture: `{"auth":{"accessToken":"old","refreshToken":"rt","expiresAt":1,"domain":"www.workbuddy.ai"},"account":{"uid":"g1"}}`,
			filename:   "workbuddy-g1.json",
			wantRealm:  "global",
		},
		{
			name: "老 CN 空 domain 落盘补 cn",
			fixture: `{"auth":{"accessToken":"old","refreshToken":"rt","expiresAt":1,"domain":""},"account":{"uid":"c1"}}`,
			filename:   "workbuddy-c1.json",
			wantRealm:  "cn",
		},
		{
			name: "已有 realm 不被覆盖——global domain 显式 cn 保持 cn",
			fixture: `{"auth":{"accessToken":"old","refreshToken":"rt","expiresAt":1,"domain":"www.workbuddy.ai","realm":"cn"},"account":{"uid":"c2"}}`,
			filename:   "workbuddy-c2.json",
			wantRealm:  "cn",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			fp := filepath.Join(dir, c.filename)
			if err := os.WriteFile(fp, []byte(c.fixture), 0o600); err != nil {
				t.Fatal(err)
			}

			a, err := auth.Parse([]byte(c.fixture))
			if err != nil {
				t.Fatal(err)
			}
			a.FilePath = fp

			p := pool.New("")
			p.Add(a)

			f := &fakeUpstream{}
			srv := f.server()
			defer srv.Close()
			up := &upstream.Client{
				HTTP:          srv.Client(),
				ChatBaseCN:    srv.URL,
				BillingBaseCN: srv.URL,
			}
			s := New(Config{Pool: p, Upstream: up})

			s.RunKeepaliveNow()
			if f.refreshCalls.Load() != 1 {
				t.Fatalf("refresh calls=%d", f.refreshCalls.Load())
			}
			raw, err := os.ReadFile(fp)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			b, err := auth.Parse(raw)
			if err != nil {
				t.Fatalf("reparse: %v", err)
			}
			if b.RealmStored() != c.wantRealm {
				t.Errorf("realm=%q want %q after refresh+save", b.RealmStored(), c.wantRealm)
			}

			// 幂等：已有标识后再跑一轮 refresh+save，值不变
			s.RunKeepaliveNow()
			raw2, err := os.ReadFile(fp)
			if err != nil {
				t.Fatalf("read after second run: %v", err)
			}
			b2, err := auth.Parse(raw2)
			if err != nil {
				t.Fatalf("reparse after second: %v", err)
			}
			if b2.RealmStored() != c.wantRealm {
				t.Errorf("realm=%q want %q after idempotent run", b2.RealmStored(), c.wantRealm)
			}
		})
	}
}

// TestRunExpiryAlertNow 巡检只挑剩余天数 <= 阈值的账号，按紧急度排序后汇总成一条通知；
// 未知到期（-1）不参与提醒，避免老账号刷屏。
func TestRunExpiryAlertNow(t *testing.T) {
	now := time.Now()
	p := pool.New("")
	// 走 auth.Parse 而不是裸结构体：RefreshExpiresAt 是在解析阶段从 JWT 现算的，
	// 直接用 &auth.Auth{} 会绕过这一步，测不到真实加载路径。
	add := func(uid, nick string, rt string) {
		raw := fmt.Sprintf(`{"accessToken":"at","refreshToken":%q,"expiresAt":1,"uid":%q,"nickname":%q}`, rt, uid, nick)
		a, err := auth.Parse([]byte(raw))
		if err != nil {
			t.Fatalf("parse %s: %v", uid, err)
		}
		p.Add(a)
	}
	mkJWT := func(exp int64) string {
		hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
		pl := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp)))
		return hdr + "." + pl + ".sig"
	}
	// 三个到期账号（3/10/24 天）+ 一个安全账号（29 天）+ 一个未知格式账号。
	add("u3d", "三天", mkJWT(now.Add(3*24*time.Hour).Unix()))
	add("u10d", "十天", mkJWT(now.Add(10*24*time.Hour).Unix()))
	add("u24d", "二四", mkJWT(now.Add(24*24*time.Hour).Unix()))
	add("u29d", "安全", mkJWT(now.Add(29*24*time.Hour).Unix()))
	add("uunknown", "未知", "opaque")

	var gotTitle, gotBody string
	calls := 0
	s := New(Config{
		Pool:           p,
		ExpiryWarnDays: 25,
		Notify: func(title, body string) {
			calls++
			gotTitle, gotBody = title, body
		},
	})
	s.RunExpiryAlertNow()

	if calls != 1 {
		t.Fatalf("Notify calls=%d want 1（每天汇总成一条）", calls)
	}
	if !strings.Contains(gotTitle, "3 个账号") {
		t.Errorf("title=%q want 含 3 个账号", gotTitle)
	}
	for _, want := range []string{"三天", "十天", "二四"} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("body 缺 %s: %q", want, gotBody)
		}
	}
	if strings.Contains(gotBody, "安全") || strings.Contains(gotBody, "未知") {
		t.Errorf("body 不应含安全/未知账号: %q", gotBody)
	}
	// 最紧急的排最前：三天在十天之前，十天在二四之前。
	i3, i10, i24 := strings.Index(gotBody, "三天"), strings.Index(gotBody, "十天"), strings.Index(gotBody, "二四")
	if !(i3 < i10 && i10 < i24) {
		t.Errorf("排序应为 3天<10天<24天，实际 body=%q", gotBody)
	}
}

// TestRunStartupCatchupRefreshesOnlyDueAccounts 启动补跑只刷 RT 剩余 <= 阈值 的账号。
//
// 为什么这条测试重要：补跑如果退化成"每次启动刷全池"，25 个账号的重启成本会翻倍，
// 而且会掩盖真正的"错过时点"信号。这里同时覆盖三个边界：
//   - 剩余 5 天 → 补刷
//   - 剩余 29 天 → 不动（刚刷过，不该多打一次上游）
//   - disabled 账号 → 不动（刷也刷不活，属于"等人工重登"）
func TestRunStartupCatchupRefreshesOnlyDueAccounts(t *testing.T) {
	f := &fakeUpstream{}
	srv := f.server()
	defer srv.Close()

	p := pool.New("")
	mkJWT := func(exp int64) string {
		hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
		pl := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp)))
		return hdr + "." + pl + ".sig"
	}
	now := time.Now()
	add := func(uid string, days int) {
		raw := fmt.Sprintf(`{"accessToken":"at","refreshToken":%q,"expiresAt":1,"uid":%q,"nickname":%q}`,
			mkJWT(now.Add(time.Duration(days)*24*time.Hour).Unix()), uid, uid)
		a, err := auth.Parse([]byte(raw))
		if err != nil {
			t.Fatalf("parse %s: %v", uid, err)
		}
		p.Add(a)
	}
	add("due", 5)
	add("fresh", 29)
	add("dead", 5)
	p.Disable("dead", "12153 session dead")

	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	s := New(Config{Pool: p, Upstream: up, ExpiryWarnDays: 25})

	restore := startupRefreshGrace
	startupRefreshGrace = 0
	defer func() { startupRefreshGrace = restore }()

	s.RunStartupCatchup(context.Background())

	if got := f.refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls=%d want 1（只刷 due 一个）", got)
	}
}

// TestRunStartupCatchupNoDueAccountsSkipsUpstream 没有到期账号时，启动补跑零上游调用。
func TestRunStartupCatchupNoDueAccountsSkipsUpstream(t *testing.T) {
	f := &fakeUpstream{}
	srv := f.server()
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "opaque-rt", ExpiresAt: 9999999999})

	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	s := New(Config{Pool: p, Upstream: up, ExpiryWarnDays: 25})

	restore := startupRefreshGrace
	startupRefreshGrace = 0
	defer func() { startupRefreshGrace = restore }()

	s.RunStartupCatchup(context.Background())
	if got := f.refreshCalls.Load(); got != 0 {
		t.Fatalf("refresh calls=%d want 0（未知到期不补刷）", got)
	}
}

// TestRunStartupCatchupStopsOnContextCancel 关机/重启窗口内 ctx 取消后不得继续打上游。
func TestRunStartupCatchupStopsOnContextCancel(t *testing.T) {
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1})
	s := New(Config{Pool: p, ExpiryWarnDays: 25})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	restore := startupRefreshGrace
	startupRefreshGrace = time.Hour
	defer func() { startupRefreshGrace = restore }()
	s.RunStartupCatchup(ctx) // 必须立即返回，不能真的等一小时
}

// TestRunExpiryAlertNowDedupesPerDay 同一天重复巡检只弹一次；跨天（标记文件内容变化）可再弹。
//
// 为什么要落盘去重：改配置、看门狗拉起、手动重启都会让进程内存态归零，
// 若只靠内存标记，"每天提醒"会退化成"每次重启提醒"。
func TestRunExpiryAlertNowDedupesPerDay(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "expiry-alert-day.txt")

	now := time.Now()
	p := pool.New("")
	raw := fmt.Sprintf(`{"accessToken":"at","refreshToken":%q,"expiresAt":1,"uid":"u5","nickname":"五天"}`,
		func() string {
			hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
			pl := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, now.Add(5*24*time.Hour).Unix())))
			return hdr + "." + pl + ".sig"
		}())
	a, err := auth.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p.Add(a)

	calls := 0
	s := New(Config{
		Pool:                 p,
		ExpiryWarnDays:       25,
		ExpiryAlertStateFile: stateFile,
		Notify:               func(string, string) { calls++ },
	})

	s.RunExpiryAlertNow()
	if calls != 1 {
		t.Fatalf("首次巡检 calls=%d want 1", calls)
	}
	s.RunExpiryAlertNow()
	if calls != 1 {
		t.Fatalf("同日重复巡检 calls=%d want 1（应去重）", calls)
	}

	// 模拟"昨天"：把标记改成前一天，再来一次应当重新提醒。
	if err := os.WriteFile(stateFile, []byte("2000-01-01"), 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}
	s.RunExpiryAlertNow()
	if calls != 2 {
		t.Fatalf("跨天巡检 calls=%d want 2", calls)
	}
}

// TestRunExpiryAlertNowNoWarnDoesNotConsumeDay 无告警时不占用当天的提醒额度：
// 当天稍晚某个账号掉到阈值以下，仍然必须能弹出来。
func TestRunExpiryAlertNowNoWarnDoesNotConsumeDay(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "expiry-alert-day.txt")

	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	pl := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(29*24*time.Hour).Unix())))
	p := pool.New("")
	a, err := auth.Parse([]byte(fmt.Sprintf(`{"accessToken":"at","refreshToken":%q,"expiresAt":1,"uid":"u29","nickname":"安全"}`, hdr+"."+pl+".sig")))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p.Add(a)

	calls := 0
	s := New(Config{
		Pool:                 p,
		ExpiryWarnDays:       25,
		ExpiryAlertStateFile: stateFile,
		Notify:               func(string, string) { calls++ },
	})
	s.RunExpiryAlertNow()
	if calls != 0 {
		t.Fatalf("calls=%d want 0", calls)
	}
	if _, err := os.Stat(stateFile); err == nil {
		t.Fatalf("无告警时不应写提醒标记")
	}

	// 阈值临时抬高到 30 天：同一个账号现在应当命中。
	s.cfg.ExpiryWarnDays = 30
	s.RunExpiryAlertNow()
	if calls != 1 {
		t.Fatalf("calls=%d want 1（无告警那次不应占用当天额度）", calls)
	}
}
