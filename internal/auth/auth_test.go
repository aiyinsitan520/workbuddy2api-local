package auth

import (
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseNested(t *testing.T) {
	raw := []byte(`{"auth":{"accessToken":"at","refreshToken":"rt","expiresAt":1753600000,"domain":""},"account":{"uid":"u1","enterpriseId":"e1","nickname":"n1"}}`)
	sa, err := Parse(raw)
	if err != nil {
		t.Fatalf("nested parse err: %v", err)
	}
	if sa.AccessToken != "at" || sa.RefreshToken != "rt" || sa.ExpiresAt != 1753600000 {
		t.Errorf("tokens: %+v", sa)
	}
	if sa.UID != "u1" || sa.EnterpriseID != "e1" || sa.Nickname != "n1" {
		t.Errorf("account: %+v", sa)
	}
}

func TestParseFlat(t *testing.T) {
	raw := []byte(`{"accessToken":"at","refreshToken":"rt","expiresAt":1753600000,"uid":"u2","nickname":"n2"}`)
	sa, err := Parse(raw)
	if err != nil || sa.UID != "u2" || sa.AccessToken != "at" {
		t.Fatalf("flat: %+v %v", sa, err)
	}
}

func TestParseMissingToken(t *testing.T) {
	if _, err := Parse([]byte(`{"uid":"u3"}`)); err == nil {
		t.Fatal("want error for missing accessToken")
	}
}

func TestSaveAtomicRoundtrip(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "workbuddy-u1.json")
	a := &Auth{AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1753600000,
		UID: "u1", EnterpriseID: "e1", Nickname: "n1", FilePath: fp}
	if err := a.SaveAtomic(); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(fp + ".tmp"); !os.IsNotExist(err) {
		t.Error("tmp file should not remain")
	}
	raw, err := os.ReadFile(fp)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	b, err := Parse(raw)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if b.AccessToken != "at" || b.UID != "u1" || b.EnterpriseID != "e1" {
		t.Errorf("roundtrip: %+v", b)
	}
}

// TestLoadDirLoadsAllValid 不再按 region 过滤：所有可解析的 auth 文件都被加载，
// 解析失败的文件静默跳过。
func TestLoadDirLoadsAllValid(t *testing.T) {
	dir := t.TempDir()
	cn := `{"auth":{"accessToken":"at1","refreshToken":"r","expiresAt":1,"domain":""},"account":{"uid":"cn1"}}`
	other := `{"auth":{"accessToken":"at2","refreshToken":"r","expiresAt":1,"domain":"example.com"},"account":{"uid":"u2"}}`
	bad := `not json`
	os.WriteFile(filepath.Join(dir, "workbuddy-cn1.json"), []byte(cn), 0o600)
	os.WriteFile(filepath.Join(dir, "workbuddy-u2.json"), []byte(other), 0o600)
	os.WriteFile(filepath.Join(dir, "workbuddy-bad.json"), []byte(bad), 0o600)

	list, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 valid accounts, got %+v", list)
	}
	for _, a := range list {
		if a.FilePath == "" {
			t.Error("FilePath not set")
		}
	}
}

func TestNeedsRefresh(t *testing.T) {
	a := &Auth{ExpiresAt: 0}
	if !a.NeedsRefresh(0) {
		t.Error("zero expiry should need refresh")
	}
	a.ExpiresAt = 9999999999
	if a.NeedsRefresh(0) {
		t.Error("far future should not need refresh")
	}
}

// TestParseDeviceToken 嵌套形与扁平形 auth 文件的顶层 device_token 键均被解析。
func TestParseDeviceToken(t *testing.T) {
	nested := []byte(`{"auth":{"accessToken":"at","refreshToken":"rt","expiresAt":1,"domain":""},"account":{"uid":"u1"},"device_token":"dev-tok-nested"}`)
	sa, err := Parse(nested)
	if err != nil {
		t.Fatalf("nested parse: %v", err)
	}
	if sa.DeviceToken != "dev-tok-nested" {
		t.Errorf("nested DeviceToken = %q want %q", sa.DeviceToken, "dev-tok-nested")
	}

	flat := []byte(`{"accessToken":"at","refreshToken":"rt","expiresAt":1,"uid":"u2","device_token":"dev-tok-flat"}`)
	fa, err := Parse(flat)
	if err != nil {
		t.Fatalf("flat parse: %v", err)
	}
	if fa.DeviceToken != "dev-tok-flat" {
		t.Errorf("flat DeviceToken = %q want %q", fa.DeviceToken, "dev-tok-flat")
	}
}

// TestSaveAtomicPreservesDeviceToken SaveAtomic 写回后顶层 device_token 被保留并重新解析回来。
func TestSaveAtomicPreservesDeviceToken(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "workbuddy-dt.json")
	a := &Auth{AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1,
		UID: "u1", DeviceToken: "persisted-tok", FilePath: fp}
	if err := a.SaveAtomic(); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(fp)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	b, err := Parse(raw)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if b.DeviceToken != "persisted-tok" {
		t.Errorf("roundtrip DeviceToken = %q want %q", b.DeviceToken, "persisted-tok")
	}
}

// TestLoadDirBackfillsRealm 存量迁移：LoadDir 加载目录时对空 realm 的 auth 自动
// backfill + SaveAtomic；已有 realm 的保持原值（不被 domain 覆盖）；文件全部带标识。
func TestLoadDirBackfillsRealm(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fixtures := map[string]string{
		"workbuddy-g1.json": `{"auth":{"accessToken":"at","refreshToken":"r","expiresAt":1,"domain":"www.workbuddy.ai"},"account":{"uid":"g1"}}`,
		"workbuddy-c1.json": `{"auth":{"accessToken":"at","refreshToken":"r","expiresAt":1,"domain":""},"account":{"uid":"c1"}}`,
		// 已有 realm 的不因 domain 变化被覆盖：global domain + 显式 cn → 保持 cn
		"workbuddy-c2.json": `{"auth":{"accessToken":"at","refreshToken":"r","expiresAt":1,"domain":"www.workbuddy.ai","realm":"cn"},"account":{"uid":"c2"}}`,
	}
	for name, body := range fixtures {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	list, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("want 3 accounts, got %d", len(list))
	}
	want := map[string]string{"g1": "global", "c1": "cn", "c2": "cn"}
	for _, a := range list {
		// 内存态已补标识
		if got := a.RealmStored(); got != want[a.UID] {
			t.Errorf("uid=%s in-memory realm=%q want %q", a.UID, got, want[a.UID])
		}
		// 落盘文件也带 realm 键
		raw, err := os.ReadFile(a.FilePath)
		if err != nil {
			t.Fatalf("read %s: %v", a.FilePath, err)
		}
		b, err := Parse(raw)
		if err != nil {
			t.Fatalf("reparse %s: %v", a.FilePath, err)
		}
		if got := b.RealmStored(); got != want[a.UID] {
			t.Errorf("uid=%s on-disk realm=%q want %q", a.UID, got, want[a.UID])
		}
	}
}

// TestLoadDirBackfillWriteFailureDoesNotBlock 单个文件 backfill 落盘失败（tmp 预置目录
// 使 WriteFile 失败）不阻断启动：其他文件照常迁移，LoadDir 不向上抛错。
// （历史纯 CN auth 目录一次性迁移时，个别文件不可写不应让整个服务起不来。）
func TestLoadDirBackfillWriteFailureDoesNotBlock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	good := `{"auth":{"accessToken":"at","refreshToken":"r","expiresAt":1,"domain":"www.workbuddy.ai"},"account":{"uid":"g1"}}`
	if err := os.WriteFile(filepath.Join(dir, "workbuddy-g1.json"), []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	// 预置同名 .tmp 目录 → SaveAtomic 的 os.WriteFile(".tmp") 报 is a directory。
	if err := os.Mkdir(filepath.Join(dir, "workbuddy-c1.json.tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	bad := `{"auth":{"accessToken":"at","refreshToken":"r","expiresAt":1},"account":{"uid":"c1"}}`
	if err := os.WriteFile(filepath.Join(dir, "workbuddy-c1.json"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}

	list, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("load err=%v want nil (write failure must not block startup)", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 accounts loaded, got %d", len(list))
	}
	// 好文件迁移成功
	raw, _ := os.ReadFile(filepath.Join(dir, "workbuddy-g1.json"))
	b, _ := Parse(raw)
	if b.RealmStored() != "global" {
		t.Errorf("good file realm=%q want global (migration should succeed)", b.RealmStored())
	}
}

// TestLoadDirDuplicateUIDWarning 同 UID 双 realm auth 文件（概率近零的 EDGE）：LoadDir
// 检测到重复 UID 时打 WARN（含两文件路径），且不改变加载行为——后载入者胜出（返回 1 个、
// 不 panic、realm 为后载入者值）。LoadDir 现在有额外 seenUID 副作用，逐字验证 WARN。
func TestLoadDirDuplicateUIDWarning(t *testing.T) {
	dir := t.TempDir()
	// 同一 UID u9 的两个文件：cn realm 文件按文件名排序在前（workbuddy-a-...），
	// global realm 文件在后 → 后载入者（global）胜出。
	cn := `{"auth":{"accessToken":"at1","refreshToken":"r","expiresAt":1,"domain":"www.codebuddy.cn"},"account":{"uid":"u9"}}`
	gl := `{"auth":{"accessToken":"at2","refreshToken":"r","expiresAt":1,"domain":"www.workbuddy.ai"},"account":{"uid":"u9"}}`
	if err := os.WriteFile(filepath.Join(dir, "workbuddy-a-cn.json"), []byte(cn), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workbuddy-z-global.json"), []byte(gl), 0o600); err != nil {
		t.Fatal(err)
	}

	// 捕获 log 输出（本测试不 t.Parallel：log.SetOutput 是进程级全局，需串行）。
	old := log.Writer()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	log.SetOutput(w)

	list, err := LoadDir(dir)
	_ = w.Close()
	raw, _ := io.ReadAll(r)
	log.SetOutput(old)

	if err != nil {
		t.Fatalf("load err=%v", err)
	}
	// 行为稳定（不改加载结果）：LoadDir 返回全部可解析文件（去重发生在 pool.SyncToDir
	// 的 UID 键 upsert），不 panic。
	if len(list) != 2 {
		t.Fatalf("want 2 accounts loaded (dedup later in pool), got %d", len(list))
	}
	// WARN 已触发且含两文件路径。
	if !strings.Contains(string(raw), "WARN: uid") ||
		!strings.Contains(string(raw), "duplicated") ||
		!strings.Contains(string(raw), "workbuddy-a-cn.json") ||
		!strings.Contains(string(raw), "workbuddy-z-global.json") {
		t.Errorf("expected WARN with both paths, got output: %s", string(raw))
	}
}

// fakeJWT 造一个只有 header/payload 有意义的 JWT：签名段随意，
// parseJWTexp 只解 payload，不验签（我们只需要读 exp）。
func fakeJWT(exp int64) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d,"iat":%d}`, exp, exp-30*86400)))
	return hdr + "." + payload + ".sig"
}

// TestParseRefreshExpiry 覆盖三种输入：正常 JWT、payload 无 exp、非 JWT。
func TestParseRefreshExpiry(t *testing.T) {
	want := time.Now().Add(20 * 24 * time.Hour).Unix()
	if got := ParseRefreshExpiry(fakeJWT(want)); got != want {
		t.Errorf("exp=%d want %d", got, want)
	}
	noExp := base64.RawURLEncoding.EncodeToString([]byte(`{"iat":1}`))
	if got := ParseRefreshExpiry("h." + noExp + ".s"); got != 0 {
		t.Errorf("missing exp = %d want 0", got)
	}
	if got := ParseRefreshExpiry("opaque-token"); got != 0 {
		t.Errorf("non-jwt = %d want 0", got)
	}
	if got := ParseRefreshExpiry(""); got != 0 {
		t.Errorf("empty = %d want 0", got)
	}
}

// TestParseFillsRefreshExpiryFromJWT 磁盘上没有 refreshExpiresAt 时（老文件），
// Parse 现算一次但不改文件；下次 SaveAtomic 才落盘。
func TestParseFillsRefreshExpiryFromJWT(t *testing.T) {
	exp := time.Now().Add(10 * 24 * time.Hour).Unix()
	raw := []byte(fmt.Sprintf(`{"accessToken":"at","refreshToken":%q,"expiresAt":1,"uid":"u1"}`, fakeJWT(exp)))
	a, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if a.RefreshExpiresAt != exp {
		t.Errorf("RefreshExpiresAt=%d want %d", a.RefreshExpiresAt, exp)
	}
	if got := a.RefreshDaysLeft(); got != 10 {
		t.Errorf("days left=%d want 10", got)
	}
}

// TestParsePrefersStoredRefreshExpiresAt 显式存了 refreshExpiresAt 时以磁盘为准：
// 换发后的 RT 若还没落盘，JWT 里的值可能与记录不一致，不能让解析覆盖记录。
func TestParsePrefersStoredRefreshExpiresAt(t *testing.T) {
	stored := int64(1800000000)
	raw := []byte(fmt.Sprintf(`{"accessToken":"at","refreshToken":%q,"expiresAt":1,"uid":"u1","refreshExpiresAt":%d}`,
		fakeJWT(1900000000), stored))
	a, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if a.RefreshExpiresAt != stored {
		t.Errorf("RefreshExpiresAt=%d want stored %d", a.RefreshExpiresAt, stored)
	}
}

// TestSaveAtomicPersistsRefreshExpiry 只在已知（>0）时写键，避免 0 污染老文件。
func TestSaveAtomicPersistsRefreshExpiry(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "workbuddy-exp.json")
	exp := time.Now().Add(5 * 24 * time.Hour).Unix()
	a := &Auth{AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1, UID: "u1",
		FilePath: fp, RefreshExpiresAt: exp}
	if err := a.SaveAtomic(); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, _ := os.ReadFile(fp)
	if !strings.Contains(string(raw), `"refreshExpiresAt"`) {
		t.Errorf("refreshExpiresAt not persisted: %s", raw)
	}
	b, err := Parse(raw)
	if err != nil || b.RefreshExpiresAt != exp {
		t.Errorf("roundtrip exp=%d want %d (err=%v)", b.RefreshExpiresAt, exp, err)
	}

	// 未知到期（0）不应写键：老文件保持干净，也不会被误读成"已过期"。
	fp2 := filepath.Join(dir, "workbuddy-unknown.json")
	a2 := &Auth{AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1, UID: "u2", FilePath: fp2}
	if err := a2.SaveAtomic(); err != nil {
		t.Fatalf("save2: %v", err)
	}
	raw2, _ := os.ReadFile(fp2)
	if strings.Contains(string(raw2), `"refreshExpiresAt"`) {
		t.Errorf("unknown expiry should not be persisted: %s", raw2)
	}
}

// TestRefreshExpiring 阈值判定：已知且进入窗口才算紧张；未知一律 false（不制造噪音）。
func TestRefreshExpiring(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		exp  int64
		prev bool
	}{
		{"unknown", 0, false},
		{"30d", now.Add(30 * 24 * time.Hour).Unix(), false},
		{"24d", now.Add(24 * 24 * time.Hour).Unix(), true},
		{"expired", now.Add(-time.Hour).Unix(), true},
	}
	for _, c := range cases {
		a := &Auth{RefreshExpiresAt: c.exp}
		if got := a.RefreshExpiring(25 * 24 * time.Hour); got != c.prev {
			t.Errorf("%s: RefreshExpiring=%v want %v", c.name, got, c.prev)
		}
	}
	// 未知天数用 -1 表达（不是 0：0 会被读成"今天到期"）。
	if got := (&Auth{}).RefreshDaysLeft(); got != -1 {
		t.Errorf("unknown days=%d want -1", got)
	}
}
