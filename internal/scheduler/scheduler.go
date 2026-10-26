// Package scheduler 定时任务：签到 / 活跃上报 / 猫猫旅行 / token keepalive / 开学季 / 夜猫子 六类独立排程。
// 签到成功后重新查余额，余额 > 0 的冷却账号自动解冻。
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/logfmt"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/upstream"
)

// Config 调度器依赖。
//
// 任务开关用「禁用」命名而非「启用」：零值 Config 即六类任务都启用（hours 回落默认），
// 与引入开关前的行为逐字一致（老调用方/老测试无需改动）。
type Config struct {
	Pool           *pool.Pool
	Upstream       *upstream.Client
	CheckinHours   []int // 默认 [9, 21]
	TravelHours    []int // 默认 [9,21]：一趟派出 + 一趟领奖闭环
	ActivityHours  []int // 默认 [10]
	KeepaliveHours []int // 默认 [22]
	SchoolHours    []int // 默认 [12]：开学季任务（迁移自系统 crontab）
	CatHours       []int // 默认 [1]：夜猫子任务（迁移自系统 crontab）
	// ActivityReportCount 每号每次活跃上报的条数：领猫前置需 5 次对话，
	// 默认 5 条同一 conversationId 内多轮上报把 chat_5 刷满；0/缺省=1 兼容旧行为。
	ActivityReportCount       int
	SkipGlobalActivityReports bool // 国际账号只执行真实任务，不模拟活跃事件。

	// ExpiringSoonWindow 快过期积分窗口：签到查余额时，把到期时间 <= now+window 的
	// 套餐余额标记为"快过期"（pool 据此优先消耗，见 entry.creditsExpiring）。
	// <=0 时禁用分桶（全部归长期，行为与引入前一致）。默认建议 7*24h。
	ExpiringSoonWindow time.Duration

	// ExpiryAlertHours 凭据到期巡检时点（本地小时），默认 [10]。
	ExpiryAlertHours []int
	// ExpiryWarnDays refresh token 剩余天数低于该值时每天提醒，默认 25 天。
	// 上游签发窗口是 30 天且每次刷新滑动续期，25 天意味着已连续 5 天没刷成功——
	// 此时仍有充足时间人工重登，不至于等到账号彻底失效才发现。
	ExpiryWarnDays int
	// ExpiryAlertDisabled 显式关闭到期巡检（schedule.expiry_alert_enabled=false）。
	ExpiryAlertDisabled bool
	// ExpiryAlertStateFile 到期巡检的"今天已提醒过"标记文件（由 main 从 StateFile 同目录派生）。
	// 空 = 不做每日去重（每次排程/启动补跑都会提醒）。
	//
	// 为什么需要落盘：进程重启（改配置、看门狗拉起、手动重启）会让内存态归零，
	// 而"每天提醒一次"的语义要求跨进程也成立——否则一天重启三次就弹三次 toast。
	ExpiryAlertStateFile string
	// Notify 桌面通知回调（可为 nil：nil 时只写日志）。由 main 注入 Windows toast，
	// 避免本包依赖平台 API——scheduler 保持纯逻辑，便于测试。
	Notify func(title, body string)

	// CheckinDisabled 显式关闭签到排程（对应 config 的 schedule.checkin_enabled=false）。
	// 禁用后不再有任何签到时点。旅行不再搭签到便车（已剥离为独立排程）。
	CheckinDisabled bool
	// TravelDisabled 显式关闭猫猫旅行排程（schedule.travel_enabled=false）。
	TravelDisabled bool
	// ActivityDisabled 显式关闭活跃上报排程（schedule.activity_enabled=false）。
	ActivityDisabled bool
	// KeepaliveDisabled 显式关闭 token 保活排程（schedule.keepalive_enabled=false）。
	KeepaliveDisabled bool
	// SchoolDisabled 显式关闭开学季任务排程（schedule.school_enabled=false）。
	SchoolDisabled bool
	// CatDisabled 显式关闭夜猫子任务排程（schedule.cat_enabled=false）。
	CatDisabled bool
}

// Scheduler 调度器。
type Scheduler struct {
	cfg Config

	// mu/adoptTried 领养当日失败记录：uid → 自然日（CST）。门槛未达的账号当日不再重试，
	// 避免同日多趟对上游重试轰炸；进程重启即清零（无需持久化）。
	mu         sync.Mutex
	adoptTried map[string]string

	// checkinMu 串行化签到：定时入口与手动触发互斥，避免同一时刻重复打上游签到接口。
	checkinMu sync.Mutex
}

// New 构建。
func New(cfg Config) *Scheduler {
	if len(cfg.CheckinHours) == 0 {
		cfg.CheckinHours = []int{9, 21}
	}
	if len(cfg.TravelHours) == 0 {
		cfg.TravelHours = []int{9, 21}
	}
	if len(cfg.ActivityHours) == 0 {
		cfg.ActivityHours = []int{10}
	}
	if len(cfg.KeepaliveHours) == 0 {
		cfg.KeepaliveHours = []int{22}
	}
	if len(cfg.SchoolHours) == 0 {
		cfg.SchoolHours = []int{12}
	}
	if len(cfg.CatHours) == 0 {
		cfg.CatHours = []int{1}
	}
	if len(cfg.ExpiryAlertHours) == 0 {
		cfg.ExpiryAlertHours = []int{10}
	}
	if cfg.ExpiryWarnDays <= 0 {
		cfg.ExpiryWarnDays = 25
	}
	// 0/缺省 = 1 条（兼容旧行为：每号每天 1 条上报点亮连登）。
	if cfg.ActivityReportCount <= 0 {
		cfg.ActivityReportCount = 1
	}
	return &Scheduler{cfg: cfg, adoptTried: make(map[string]string)}
}

// checkinRefreshSkew 签到前判定"token 是否临近过期"的时间窗口（10 分钟）。
// 长时间停机/容器长期停跑后 access token 往往已过期，不先刷新则签到必然 401 白跑。
const checkinRefreshSkew = 10 * time.Minute

// CheckinStatus 单账号签到结果状态。
type CheckinStatus string

const (
	CheckinOK      CheckinStatus = "ok"      // 签到成功
	CheckinAlready CheckinStatus = "already" // 上游判定今天已签到（幂等重复，视为正常）
	CheckinFail    CheckinStatus = "fail"    // 刷新 token / 签到 / 余额查询失败
	CheckinSkipped CheckinStatus = "skipped" // 禁用账号或无有效凭证，未参与
)

// CheckinOutcome 单账号签到结果（供手动签到回执与日志汇总）。
type CheckinOutcome struct {
	UID      string        `json:"uid"`
	Nickname string        `json:"nickname,omitempty"`
	Status   CheckinStatus `json:"status"`
	Credits  *int64        `json:"credits,omitempty"` // 签到后余额（余额查询成功才有值）
	Detail   string        `json:"detail,omitempty"`  // 失败/跳过原因（"已签到"不填）
}

// ErrBusy 已有一次签到正在执行（手动入口与定时撞车）。
var ErrBusy = errors.New("checkin already running")

// nextFire 返回 now 之后最近的一个整点触发时间；hours 为本地小时（0-23）。
func nextFire(now time.Time, hours []int) time.Time {
	var earliest time.Time
	for _, h := range hours {
		t := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, now.Location())
		if !t.After(now) {
			t = t.Add(24 * time.Hour)
		}
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	return earliest
}

// taskKind 调度任务类型。
type taskKind int

const (
	taskCheckin taskKind = iota
	taskTravel
	taskActivity
	taskKeepalive
	taskSchool
	taskCat
	taskExpiryAlert
)

// nextWake 返回 now 之后最近的唤醒时刻，以及该时刻需要执行的全部任务。
// 多类任务若配到同一小时（如签到与旅行都含 9），该时刻多类任务需一并执行。
// 已显式禁用的任务不进候选（nextFire 对其零值返回零时间，nextWake 再跳过零时点）。
func (s *Scheduler) nextWake(now time.Time) (time.Time, []taskKind) {
	type slot struct {
		at   time.Time
		kind taskKind
	}
	var slots []slot
	if !s.cfg.CheckinDisabled {
		slots = append(slots, slot{nextFire(now, s.cfg.CheckinHours), taskCheckin})
	}
	if !s.cfg.TravelDisabled {
		slots = append(slots, slot{nextFire(now, s.cfg.TravelHours), taskTravel})
	}
	if !s.cfg.ActivityDisabled {
		slots = append(slots, slot{nextFire(now, s.cfg.ActivityHours), taskActivity})
	}
	if !s.cfg.KeepaliveDisabled {
		slots = append(slots, slot{nextFire(now, s.cfg.KeepaliveHours), taskKeepalive})
	}
	if !s.cfg.SchoolDisabled {
		slots = append(slots, slot{nextFire(now, s.cfg.SchoolHours), taskSchool})
	}
	if !s.cfg.CatDisabled {
		slots = append(slots, slot{nextFire(now, s.cfg.CatHours), taskCat})
	}
	if !s.cfg.ExpiryAlertDisabled {
		slots = append(slots, slot{nextFire(now, s.cfg.ExpiryAlertHours), taskExpiryAlert})
	}
	var earliest time.Time
	for _, sl := range slots {
		if sl.at.IsZero() {
			continue
		}
		if earliest.IsZero() || sl.at.Before(earliest) {
			earliest = sl.at
		}
	}
	if earliest.IsZero() {
		return time.Time{}, nil
	}
	var kinds []taskKind
	for _, sl := range slots {
		if !sl.at.IsZero() && sl.at.Equal(earliest) {
			kinds = append(kinds, sl.kind)
		}
	}
	return earliest, kinds
}

// Run 主循环，阻塞直到 ctx 取消。
func (s *Scheduler) Run(ctx context.Context) {
	for {
		next, kinds := s.nextWake(time.Now())
		if next.IsZero() {
			// 六类任务全部禁用：不空转，只等退出信号。
			<-ctx.Done()
			return
		}
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			// 到点任务在排程时确定（不依赖唤醒时刻的小时数），迟到唤醒也不会漏跑。
			for _, k := range kinds {
				s.dispatch(k)
			}
		}
	}
}

// dispatch 按任务类型分发到对应执行函数。脚本类（school/cat）失败只记 WARN、
// 不影响其余任务继续执行（与现有各任务"单账号失败不阻断遍历"同口径）。
func (s *Scheduler) dispatch(k taskKind) {
	switch k {
	case taskCheckin:
		s.RunCheckinNow()
	case taskTravel:
		s.RunTravelNow()
	case taskActivity:
		s.RunActivityNow()
	case taskKeepalive:
		s.RunKeepaliveNow()
	case taskSchool:
		s.RunSchoolNow()
	case taskCat:
		s.RunCatNow()
	case taskExpiryAlert:
		s.RunExpiryAlertNow()
	}
}

// RunCheckinNow 定时触发的立即签到：逐账号结果由 CheckinAll 记日志，此处只兜住"撞车跳过"。
func (s *Scheduler) RunCheckinNow() {
	if _, err := s.CheckinAll(); err != nil {
		log.Printf("scheduled checkin skipped: %v", err)
	}
}

// CheckinAll 全量签到：按需刷新 token → daily-checkin → 查余额 → 解冻冷却账号。
// 冷却中的账号也参与（签到就是为了解冻它们）；禁用的跳过。
// 同一时刻只允许一次签到在跑，重复调用返回 ErrBusy（防止手动触发与定时撞车重复打上游）。
//
// session dead 走 Pool.NoteSessionDead 的**连续计数**语义（与 keepalive 一致）：
// 一次刷新失败不再立即杀号，连续 sessionDeadThreshold 次才禁用，刷新成功清计数。
func (s *Scheduler) CheckinAll() ([]CheckinOutcome, error) {
	if !s.checkinMu.TryLock() {
		return nil, ErrBusy
	}
	defer s.checkinMu.Unlock()

	statuses := s.cfg.Pool.List()
	out := make([]CheckinOutcome, 0, len(statuses))
	var okN, alreadyN, failN, skipN int
	for _, st := range statuses {
		oc := CheckinOutcome{UID: st.UID, Nickname: st.Nickname}
		if st.Disabled {
			oc.Status, oc.Detail = CheckinSkipped, "disabled"
			skipN++
			out = append(out, oc)
			continue
		}
		a := s.cfg.Pool.AuthByUID(st.UID)
		if a == nil || a.RefreshToken == "" {
			oc.Status, oc.Detail = CheckinSkipped, "no credentials"
			skipN++
			out = append(out, oc)
			continue
		}
		// D4 门控：realm=global 账号无签到体系/任务中心，直接跳过（不发起任何上游调用，避免风控）。
		// 经 auth.Realm() 统一判定：逃生门（global.enabled=false）下 global 账号被降级为 cn、
		// 按 CN 处理——这是 D5 逃生门的刻意语义（纯 CN 部署锁死一切 global），与引用处一致。
		if a.IsGlobal() {
			oc.Status, oc.Detail = CheckinSkipped, "global"
			skipN++
			out = append(out, oc)
			continue
		}
		// 停机跨过 token 有效期（关机过夜/容器长期停跑）时先补一次刷新，否则签到必然 401 白跑。
		if a.NeedsRefresh(checkinRefreshSkew) {
			if err := s.cfg.Upstream.RefreshToken(a); err != nil {
				log.Printf("checkin %s refresh: %v", logfmt.UID8(st.UID), err)
				var ue *upstream.Error
				if errors.As(err, &ue) && ue.Kind == upstream.ErrSessionDead {
					if s.cfg.Pool.NoteSessionDead(st.UID) {
						log.Printf("WARN: checkin %s: 连续 %d 次 12153 session dead — 禁用", logfmt.UID8(st.UID), pool.SessionDeadThreshold())
					}
				}
				// 刷新只是"提前补票"：token 若仍有效，继续照常签到（否则刷新接口抖动
				// 会让本可成功的签到被白白跳过）；真正过期才判定失败。
				if a.NeedsRefresh(0) {
					oc.Status, oc.Detail = CheckinFail, "refresh: "+err.Error()
					failN++
					out = append(out, oc)
					continue
				}
			} else {
				a.BackfillRealm() // 老 auth 空 realm → 落盘前补标识（幂等：已有不动）
				if err := a.SaveAtomic(); err != nil {
					// 刷新成功但落盘失败：重启会用旧 token，必须暴露。
					log.Printf("checkin %s save: %v", logfmt.UID8(st.UID), err)
				}
			}
		}
		// 签到返回错误（含"今天已签到"）也继续查余额：余额恢复即可解冻账号。
		if err := s.cfg.Upstream.DailyCheckin(a); err != nil {
			if upstream.IsAlreadyCheckin(err) {
				// "今天已签到"是幂等成功，不是错误：不填 detail，免得回执里
				// 出现一整段 400 报文、被误读成签到失败。
				oc.Status = CheckinAlready
			} else {
				oc.Status = CheckinFail
				oc.Detail = err.Error()
				log.Printf("checkin %s: %v", logfmt.UID8(st.UID), err)
			}
		} else {
			oc.Status = CheckinOK
		}
		// 分桶查余额：快过期窗口内的积分单独标记，pool 优先消耗（issue:积分过期）。
		// ExpiringSoonWindow<=0 时退化为纯总量（与引入前一致）。
		remain, buckets, err := s.cfg.Upstream.UserResourceDetailed(a, s.cfg.ExpiringSoonWindow)
		if err != nil {
			log.Printf("user-resource %s: %v", logfmt.UID8(st.UID), err)
			oc.Status = CheckinFail
			oc.Detail = joinDetail(oc.Detail, "resource: "+err.Error())
			failN++
			out = append(out, oc)
			continue
		}
		s.cfg.Pool.ReenableIfCredits(st.UID, remain)
		s.cfg.Pool.SetCreditsDetailed(st.UID, remain, buckets.Expiring)
		oc.Credits = &remain
		switch oc.Status {
		case CheckinOK:
			okN++
		case CheckinAlready:
			alreadyN++
		default:
			failN++
		}
		out = append(out, oc)
	}
	log.Printf("checkin done: total=%d ok=%d already=%d fail=%d skipped=%d",
		len(statuses), okN, alreadyN, failN, skipN)
	return out, nil
}

// joinDetail 拼接多段原因，避免后一段覆盖前一段的失败信息。
func joinDetail(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}

// RunActivityNow 立即对池内所有可用账号执行对话活跃上报。
// 禁用账号跳过；无 AccessToken 的跳过；账号间限速 activityAccountDelay。
// CN 与 global 账号**都上报**（PR #45 实测国际版 /v2/report 可用）；单账号失败
// 只记 WARN 不影响遍历。
//
// 每号上报 N 条（ActivityReportCount，默认 5）：N 条共用同一 conversationId
// （wb2api-<ms>），模拟同一会话内 N 轮对话——这是领养猫（buddy/first）对话量
// 门槛的实测刷法（chat_5 前置需 5 次对话）。requestId 各条独立（同会话多轮）。
// 账号内 N 条之间间隔 activityReportGap（1.5s）避免秒发触发风控。
//
// 0/缺省 ActivityReportCount = 1 条，兼容旧行为（仅点亮连登 + 解锁 first_buddy）。
//
// 上报成功后：① streak 自检（回读连登，发现「200 但静默丢弃」）；
// ② 无猫账号立即重试领养（travelAdoptForce）——对话量刚补满的新状态，不算重试，
// 豁免 adoptTriedToday 当日防抖（旅行排程 09 点已领养过且 skip，10 点上报补满后
// 不能依赖下一轮旅行领养，就地闭环）。
func (s *Scheduler) RunActivityNow() {
	count := s.cfg.ActivityReportCount
	first := true
	for _, st := range s.cfg.Pool.List() {
		if st.Disabled {
			continue
		}
		a := s.cfg.Pool.AuthByUID(st.UID)
		if a == nil || a.AccessToken == "" {
			continue
		}
		if s.cfg.SkipGlobalActivityReports && a.IsGlobal() {
			continue
		}
		// global 账号同样上报（PR #45 实测国际版 /v2/report 在 workbuddy.ai 上 code=0 OK，
		// 点亮连登）；realmBase 路由/头由 upstream.billingJSON/BillingHeaders 按 realm 切。
		// 单账号失败只记 WARN 不影响遍历（下方 report err → break 该号 → continue 下号）。
		if !first {
			time.Sleep(activityAccountDelay)
		}
		first = false
		// N 条共用同一 conversationId（同会话），requestId 各自独立（每条一个）。
		cid := fmt.Sprintf("wb2api-%d", time.Now().UnixMilli())
		ok := 0
		for i := 1; i <= count; i++ {
			rid := fmt.Sprintf("%s-r%d", cid, i)
			if err := s.cfg.Upstream.ReportChatActivity(a, cid, rid); err != nil {
				log.Printf("activity %s: report %d/%d: %v", logfmt.UID8(a.UID), i, count, err)
				break // 本号上报失败：不再续发，streak 自检无意义
			}
			log.Printf("activity %s: report %d/%d ok", logfmt.UID8(a.UID), i, count)
			ok++
			if i < count {
				time.Sleep(activityReportGap) // 账号内 5 条之间间隔，避免秒发风控
			}
		}
		if ok < count {
			continue // N 条未发满：streak 自检与领养均无意义，下个账号
		}
		s.checkActivityStreak(a) // N 条全发满 → 回读 streak 自检（只留结论行）
		s.travelAdoptForce(a)    // 无猫账号对话量刚补满 → 立即重试领养（豁免防抖）
	}
}

// checkActivityStreak 上报成功后回读连登天数（只读 oracle，发现静默失败）。
// 背景：REPORT-active-map.md §2 实测「上报 200 但静默丢弃」（缺 userId 时 progress 不动），
// 上报 200 ≠ streak 计分——需要回读验证闭环。
// 异常检测口径：days==0 → warn（report OK but streak.days=0 (silent drop?)）；
// GET 失败 → warn 但不影响主流程（上报本身已成功，按天幂等，不做重试）。
// 日志每号一行、一眼可 grep：`activity %s: streak days=%d`（成功也打，方便对账）。
// 返回 true 表示「上报 OK 但 streak 可疑」（days==0 或回读失败），供测试断言。
func (s *Scheduler) checkActivityStreak(a *auth.Auth) bool {
	days, err := s.cfg.Upstream.GrowthStreak(a)
	if err != nil {
		log.Printf("WARN: activity %s: streak check failed (report OK): %v", logfmt.UID8(a.UID), err)
		return true
	}
	if days == 0 {
		log.Printf("WARN: activity %s: report OK but streak.days=0 (silent drop?)", logfmt.UID8(a.UID))
		return true
	}
	log.Printf("activity %s: streak days=%d", logfmt.UID8(a.UID), days)
	return false
}

// RunKeepaliveNow 立即对所有账号刷新 token；session 死亡的自动禁用。
// 12153 禁用走 Pool.NoteSessionDead 的**连续计数**语义：一次刷新失败不再立即杀号，
// 连续 sessionDeadThreshold 次（3 次）才禁用（P0-1：13 个 disabled 号全是历史误判）。
// 刷新成功 → ClearSessionDead 清计数（错误判定的账号有复活路径）。
func (s *Scheduler) RunKeepaliveNow() {
	for _, st := range s.cfg.Pool.List() {
		s.refreshOne(st)
	}
}

// refreshOne 刷新一个账号的凭据；不满足条件（已禁用 / 无 RT）时直接返回。
// 抽出来是为了让"全池保活"和"启动补跑"共用同一份 12153 计数与落盘语义——
// 两条路径对同一个账号的判定必须完全一致，否则补跑会成为绕过阈值的后门。
func (s *Scheduler) refreshOne(st pool.Status) {
	if st.Disabled {
		return
	}
	a := s.cfg.Pool.AuthByUID(st.UID)
	if a == nil || a.RefreshToken == "" {
		return
	}
	if err := s.cfg.Upstream.RefreshToken(a); err != nil {
		log.Printf("keepalive %s: %v", logfmt.UID8(st.UID), err)
		var ue *upstream.Error
		if errors.As(err, &ue) && ue.Kind == upstream.ErrSessionDead {
			if s.cfg.Pool.NoteSessionDead(st.UID) {
				log.Printf("WARN: keepalive %s: 连续 %d 次 12153 session dead — 禁用", logfmt.UID8(st.UID), pool.SessionDeadThreshold())
			}
		}
		return
	}
	s.cfg.Pool.ClearSessionDead(st.UID) // 刷新成功清误判计数，失败不该累计
	a.BackfillRealm()                   // 老 auth 空 realm → 落盘前补标识（幂等：已有不动）
	if err := a.SaveAtomic(); err != nil {
		log.Printf("keepalive %s save: %v", logfmt.UID8(st.UID), err)
	}
	// 每次成功刷新都记录新的 RT 到期日：这是"滑动续期成功"的唯一证据，
	// 也是排查"为什么某天开始提醒"的对照基线（正常应恒为 30 天左右）。
	log.Printf("keepalive %s: ok, refresh token 剩余 %d 天", logfmt.UID8(st.UID), a.RefreshDaysLeft())
}

// startupRefreshGrace 启动后等待多久再补跑 keepalive。
//
// 先让 HTTP 服务起来：补跑会逐号打上游刷新，没必要和启动后的首批真实请求抢连接。
// 用 var 而非 const，测试里可缩到 0。
var startupRefreshGrace = 45 * time.Second

// RunStartupCatchup 启动后补跑一次可能"错过"的凭据续期。
//
// 为什么需要：排程只看未来时点（nextWake 从 now 往后找），进程在 22:00 不在线时那次
// keepalive 就被直接跳过，没有任何补跑机制。单次错过本身无害（RT 窗口 30 天），但
// 「确保定时刷新」不能依赖"机器恰好开着"——关机过夜、看门狗重启都可能跨过时点。
//
// 触发条件只看 RT 剩余天数 <= ExpiryWarnDays：那意味着至少连续 5 天没刷成功，正是该补刷的时刻；
// 刚刷过的账号（剩余 ~30 天）不会被无谓地多打一次上游。disabled 账号跳过（刷也刷不活）。
func (s *Scheduler) RunStartupCatchup(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(startupRefreshGrace):
	}
	window := time.Duration(s.cfg.ExpiryWarnDays) * 24 * time.Hour
	var due []pool.Status
	var names []string
	for _, st := range s.cfg.Pool.List() {
		if st.Disabled {
			continue
		}
		a := s.cfg.Pool.AuthByUID(st.UID)
		if a == nil || a.RefreshToken == "" {
			continue
		}
		if !a.RefreshExpiring(window) {
			continue
		}
		due = append(due, st)
		names = append(names, logfmt.UID8(st.UID))
	}
	if len(due) > 0 {
		log.Printf("startup catch-up: %d 个账号 refresh token 剩余 <= %d 天（%s），补跑刷新",
			len(due), s.cfg.ExpiryWarnDays, strings.Join(names, " "))
		for _, st := range due {
			s.refreshOne(st)
		}
	}
	// 无论是否补刷，都补一次到期巡检：机器 10:00 关机时那次提醒同样会被排程跳过。
	// RunExpiryAlertNow 自带"今天已提醒过"去重，正常开机日不会重复弹。
	// 显式关闭巡检（expiry_alert_enabled=false）时启动也不补——开关对两条路径必须一致。
	if !s.cfg.ExpiryAlertDisabled {
		s.RunExpiryAlertNow()
	}
}

// alertedToday 判断今天是否已经弹过到期提醒。
//
// 语义边界：读失败一律当作"没提醒过"——宁可多弹一次，也不要因为一个坏文件
// 让"每天必须提醒"静默失效。这是本系统里唯一"漏掉就不可逆"的提醒。
func (s *Scheduler) alertedToday() bool {
	if s.cfg.ExpiryAlertStateFile == "" {
		return false
	}
	raw, err := os.ReadFile(s.cfg.ExpiryAlertStateFile)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(raw)) == today()
}

// markAlertedToday 记录今天的提醒已发出（写失败只打日志：提醒本身已经发出）。
func (s *Scheduler) markAlertedToday() {
	if s.cfg.ExpiryAlertStateFile == "" {
		return
	}
	if err := os.WriteFile(s.cfg.ExpiryAlertStateFile, []byte(today()), 0o644); err != nil {
		log.Printf("expiry: 记录提醒状态失败（不影响本次提醒）: %v", err)
	}
}

// today 返回本地日期 YYYY-MM-DD。用日期而非时间戳：跨午夜必须能再弹一次。
func today() string { return time.Now().Format("2006-01-02") }

// RunExpiryAlertNow 巡检全部账号的 refresh token 剩余天数，把即将到期/已失效的账号
// 汇总成一条提醒（每天一次，不按账号刷屏）。
//
// 为什么需要它：refresh token 到期是账号池里**唯一不可逆**的失效——access token 过期
// 随时能刷回来，refresh token 一过期就只能人工重登。而 keepalive 是每天 22:00 跑一次，
// 期间任何失败都不会有人知道：日志里只有一行 keepalive 错误，不会有人主动去看。
// 这个巡检把"还剩几天"变成主动推送，让用户在最坏情况发生前有充足时间处理。
func (s *Scheduler) RunExpiryAlertNow() {
	if s.alertedToday() {
		log.Printf("expiry: 今天（%s）已提醒过，跳过重复提醒", today())
		return
	}
	type warn struct {
		nick   string
		uid    string
		days   int
		dead   bool // 已被判失效（12153 等）：刷新救不回，只能重登
		reason string
	}
	var warns []warn
	var unknown int
	for _, st := range s.cfg.Pool.List() {
		a := s.cfg.Pool.AuthByUID(st.UID)
		if a == nil {
			continue
		}
		days := a.RefreshDaysLeft()
		nick := st.Nickname
		if nick == "" {
			nick = logfmt.UID8(st.UID)
		}
		// 已失效账号优先级最高：说"还剩 N 天"是误导——它们的 refresh token
		// 已经被上游拒绝，再等也不会自己好，只能人工重登。
		if st.Disabled {
			warns = append(warns, warn{nick: nick, uid: st.UID, days: days, dead: true, reason: st.DisabledReason})
			log.Printf("WARN: expiry %s: 账号已失效（%s），需重新登录", logfmt.UID8(st.UID), st.DisabledReason)
			continue
		}
		if days < 0 {
			unknown++
			continue
		}
		if days > s.cfg.ExpiryWarnDays {
			continue
		}
		warns = append(warns, warn{nick: nick, uid: st.UID, days: days})
		log.Printf("WARN: expiry %s: refresh token 剩余 %d 天（阈值 %d 天）", logfmt.UID8(st.UID), days, s.cfg.ExpiryWarnDays)
	}
	if unknown > 0 {
		log.Printf("expiry: %d 个账号无法解析 refresh token 到期日（非 JWT 格式），未纳入巡检", unknown)
	}
	if len(warns) == 0 {
		log.Printf("expiry: 全部账号 refresh token 剩余 > %d 天", s.cfg.ExpiryWarnDays)
		return
	}
	// 排序：已失效的排最前（现在就得动手），其余按剩余天数升序。
	// 已失效账号的 days 可能是 -1（解析不到），统一压到组尾，避免"未知"抢到头名。
	rank := func(w warn) (int, int) {
		if w.dead {
			if w.days < 0 {
				return 0, 1 << 20
			}
			return 0, w.days
		}
		return 1, w.days
	}
	for i := 1; i < len(warns); i++ {
		for j := i; j > 0; j-- {
			g1, d1 := rank(warns[j])
			g0, d0 := rank(warns[j-1])
			if g1 > g0 || (g1 == g0 && d1 >= d0) {
				break
			}
			warns[j], warns[j-1] = warns[j-1], warns[j]
		}
	}
	lines := make([]string, 0, len(warns))
	deadCount := 0
	for _, w := range warns {
		if w.dead {
			deadCount++
			lines = append(lines, fmt.Sprintf("%s（%s）已失效，需重新登录", w.nick, logfmt.UID8(w.uid)))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s（%s）还剩 %d 天", w.nick, logfmt.UID8(w.uid), w.days))
	}
	var title string
	switch {
	case deadCount == len(warns):
		title = fmt.Sprintf("WorkBuddy 账号已失效：%d 个，需重新登录", deadCount)
	default:
		title = fmt.Sprintf("WorkBuddy 凭据即将到期：%d 个账号", len(warns))
	}
	body := strings.Join(lines, "\n") + "\n\n请尽快重新登录这些账号，否则到期后只能人工重登。"
	if s.cfg.Notify != nil {
		s.cfg.Notify(title, body)
	}
	// 只在真的弹了提醒后才记"今天已提醒"：没有告警时不该占用当天的提醒额度，
	// 否则当天稍晚某个账号掉到 25 天以下就再也弹不出来了。
	s.markAlertedToday()
	log.Printf("WARN: expiry alert: %s", strings.Join(lines, "；"))
}
