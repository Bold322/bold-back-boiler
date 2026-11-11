package email

import (
	"bytes"
	"fmt"
	htmltemplate "html/template"
	"text/template"
)

const inviteSubject = "You're invited to %s"

const inviteTextTemplate = `Hello,

{{.InviterName}} invited you to join {{.TenantName}}.

Click the link below to set your password and accept the invitation:

  {{.AcceptURL}}

This link expires at {{.ExpiresAt.Format "2006-01-02 15:04 UTC"}}.

If you did not expect this invitation, you can ignore this email.
`

const inviteHTMLTemplate = `<!doctype html>
<html>
<body>
<p>Hello,</p>
<p><strong>{{.InviterName}}</strong> invited you to join <strong>{{.TenantName}}</strong>.</p>
<p>Click the link below to set your password and accept the invitation:</p>
<p><a href="{{.AcceptURL}}">{{.AcceptURL}}</a></p>
<p>This link expires at {{.ExpiresAt.Format "2006-01-02 15:04 UTC"}}.</p>
<p style="color:#666;font-size:smaller">If you did not expect this invitation, you can ignore this email.</p>
</body>
</html>`

const passwordResetSubject = "Reset your password"

const passwordResetTextTemplate = `Hello,

We received a request to reset your password. Open the link below
to choose a new password:

  {{.ResetURL}}

This link expires at {{.ExpiresAt.Format "2006-01-02 15:04 UTC"}}. If you did
not request a reset you can ignore this email.
`

const passwordResetHTMLTemplate = `<!doctype html>
<html>
<body>
<p>Hello,</p>
<p>We received a request to reset your password. Click below to choose a new password:</p>
<p><a href="{{.ResetURL}}">{{.ResetURL}}</a></p>
<p>This link expires at {{.ExpiresAt.Format "2006-01-02 15:04 UTC"}}.</p>
<p style="color:#666;font-size:smaller">If you did not request a reset, you can ignore this email.</p>
</body>
</html>`

func renderPasswordReset(in PasswordResetEmail) (string, string, string, error) {
	subject := passwordResetSubject

	var textBuf bytes.Buffer
	tt, err := template.New("text").Parse(passwordResetTextTemplate)
	if err != nil {
		return "", "", "", err
	}
	if err := tt.Execute(&textBuf, in); err != nil {
		return "", "", "", err
	}

	var htmlBuf bytes.Buffer
	ht, err := htmltemplate.New("html").Parse(passwordResetHTMLTemplate)
	if err != nil {
		return "", "", "", err
	}
	if err := ht.Execute(&htmlBuf, in); err != nil {
		return "", "", "", err
	}

	return subject, textBuf.String(), htmlBuf.String(), nil
}

// renderInvite returns (subject, textBody, htmlBody, error).
func renderInvite(in InviteEmail) (string, string, string, error) {
	subject := fmt.Sprintf(inviteSubject, in.TenantName)

	var textBuf bytes.Buffer
	tt, err := template.New("text").Parse(inviteTextTemplate)
	if err != nil {
		return "", "", "", err
	}
	if err := tt.Execute(&textBuf, in); err != nil {
		return "", "", "", err
	}

	var htmlBuf bytes.Buffer
	ht, err := htmltemplate.New("html").Parse(inviteHTMLTemplate)
	if err != nil {
		return "", "", "", err
	}
	if err := ht.Execute(&htmlBuf, in); err != nil {
		return "", "", "", err
	}

	return subject, textBuf.String(), htmlBuf.String(), nil
}
