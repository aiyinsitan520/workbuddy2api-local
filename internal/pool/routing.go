package pool

import (
	"time"
	"workbuddy2api/internal/auth"
)

// Routing exclusions never disable credential maintenance or reward tasks.
func (p *Pool) SetRoutingPaused(uid string, paused bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.byUID[uid]; ok {
		e.routingPaused = paused
		p.dirty.Store(true)
	}
}

// Only the authenticated local management adapter can create an automation context.
// Security disable, cooldown, breaker, and in-flight limits remain effective.
func (p *Pool) PickForAutomationByUID(uid, model string) *auth.Auth {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byUID[uid]
	if !ok || !e.healthyForModel(time.Now(), model) || p.inFlightFull(e) {
		return nil
	}
	e.lastUsed = time.Now()
	return e.a
}
