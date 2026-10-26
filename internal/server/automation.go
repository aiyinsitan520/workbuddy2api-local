package server

import (
	"context"
	"net/http"
)

type automationContextKey struct{}

func WithAutomationRequest(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), automationContextKey{}, true))
}

func isAutomationRequest(r *http.Request) bool {
	allowed, _ := r.Context().Value(automationContextKey{}).(bool)
	return allowed && r.Header.Get("X-Workbench-Account-UID") != ""
}
