package email

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRenderInvite(t *testing.T) {
	in := InviteEmail{
		To:          "bob@example.com",
		TenantName:  "Acme Corp",
		InviterName: "Alice",
		AcceptURL:   "https://app.example.com/invite?token=abc",
		ExpiresAt:   time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC),
	}
	subject, text, html, err := renderInvite(in)
	if err != nil {
		t.Fatalf("renderInvite: %v", err)
	}
	if !strings.Contains(subject, "Acme Corp") {
		t.Errorf("subject missing tenant name: %q", subject)
	}
	if !strings.Contains(text, "Alice") || !strings.Contains(text, "Acme Corp") {
		t.Errorf("text body missing fields: %q", text)
	}
	if !strings.Contains(html, "https://app.example.com/invite?token=abc") {
		t.Errorf("html body missing link: %q", html)
	}
}

func TestNoopSender(t *testing.T) {
	s := NoopSender{}
	err := s.SendInvite(context.Background(), InviteEmail{
		To: "bob@example.com", TenantName: "Acme", AcceptURL: "https://x", ExpiresAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("noop send: %v", err)
	}
}
