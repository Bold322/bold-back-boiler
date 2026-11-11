package email

import (
	"context"
	"time"
)

// InviteEmail is the payload for an outbound worker invite. The Sender impl
// renders it against the templates in templates.go.
type InviteEmail struct {
	To          string
	TenantName  string
	InviterName string
	AcceptURL   string
	ExpiresAt   time.Time
}

// PasswordResetEmail is the payload for the "forgot password" flow.
type PasswordResetEmail struct {
	To        string
	ResetURL  string
	ExpiresAt time.Time
}

type Sender interface {
	SendInvite(ctx context.Context, in InviteEmail) error
	SendPasswordReset(ctx context.Context, in PasswordResetEmail) error
}
