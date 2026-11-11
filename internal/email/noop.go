package email

import (
	"context"
	"log/slog"
)

// NoopSender logs every send instead of dispatching. Used when
// EMAIL_DELIVERY=off (the default) so local dev does not need AWS creds.
type NoopSender struct{}

func (NoopSender) SendInvite(_ context.Context, in InviteEmail) error {
	slog.Info("email noop: invite",
		"to", in.To,
		"tenant", in.TenantName,
		"accept_url", in.AcceptURL,
		"expires_at", in.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"),
	)
	return nil
}

func (NoopSender) SendPasswordReset(_ context.Context, in PasswordResetEmail) error {
	slog.Info("email noop: password reset",
		"to", in.To,
		"reset_url", in.ResetURL,
		"expires_at", in.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"),
	)
	return nil
}
