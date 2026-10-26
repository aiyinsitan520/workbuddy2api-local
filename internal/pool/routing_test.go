package pool

import (
	"os"
	"path/filepath"
	"testing"
	"time"
	"workbuddy2api/internal/auth"
)

func TestRoutingPauseIndependentOfDisableAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	p := New(path)
	p.Add(&auth.Auth{UID: "u", AccessToken: "token"})
	p.SetRoutingPaused("u", true)
	st, _ := p.Status("u")
	if st.Disabled || !st.RoutingPaused {
		t.Fatalf("state %+v", st)
	}
	if p.Pick() != nil || p.PickByUID("u") != nil || p.PickByUIDForModel("u", "m") != nil || len(p.AvailableUIDs()) != 0 || len(p.AvailableUIDsForModel("m")) != 0 || p.ServableNow() {
		t.Fatal("ordinary selection bypassed exclusion")
	}
	if p.PickForAutomationByUID("u", "m") == nil {
		t.Fatal("task was blocked by routing pause")
	}
	p.ReenableIfCredits("u", 100)
	if p.Pick() != nil {
		t.Fatal("check-in revived ordinary routing")
	}
	p.Cooldown("u", CoolSoft, time.Minute, "limit")
	if p.Pick() != nil || p.PickForAutomationByUID("u", "m") != nil {
		t.Fatal("cooldown guard bypassed")
	}
	p.Close()
	p = New(path)
	defer p.Close()
	p.Add(&auth.Auth{UID: "u", AccessToken: "token"})
	st, _ = p.Status("u")
	if !st.RoutingPaused || st.Disabled {
		t.Fatal("exclusion not persisted")
	}
	p.Disable("u", "session dead")
	p.SetRoutingPaused("u", false)
	if p.PickForAutomationByUID("u", "m") != nil {
		t.Fatal("credential disable bypassed")
	}
	st, _ = p.Status("u")
	if !st.Disabled {
		t.Fatal("routing enable revived invalid credential")
	}
}

func TestLegacyManualPauseMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"accounts":{"manual":{"disabled":true,"reason":"手动暂停"},"invalid":{"disabled":true,"reason":"session dead"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	p := New(path)
	defer p.Close()
	manual, _ := p.Status("manual")
	invalid, _ := p.Status("invalid")
	if manual.Disabled || !manual.RoutingPaused || !invalid.Disabled || invalid.RoutingPaused {
		t.Fatalf("manual %+v invalid %+v", manual, invalid)
	}
}
