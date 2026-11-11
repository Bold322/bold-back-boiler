package email

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// SESSender uses AWS SES v2 to deliver emails. Credentials come from the
// default AWS credential chain (same as the S3 client).
type SESSender struct {
	client      *sesv2.Client
	fromAddress string
}

type SESConfig struct {
	Region      string
	FromAddress string
}

func NewSESSender(ctx context.Context, cfg SESConfig) (*SESSender, error) {
	if cfg.FromAddress == "" {
		return nil, fmt.Errorf("SES from address required")
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	return &SESSender{
		client:      sesv2.NewFromConfig(awsCfg),
		fromAddress: cfg.FromAddress,
	}, nil
}

func (s *SESSender) SendInvite(ctx context.Context, in InviteEmail) error {
	subject, text, html, err := renderInvite(in)
	if err != nil {
		return fmt.Errorf("render invite: %w", err)
	}
	return s.send(ctx, in.To, subject, text, html, "send invite")
}

func (s *SESSender) SendPasswordReset(ctx context.Context, in PasswordResetEmail) error {
	subject, text, html, err := renderPasswordReset(in)
	if err != nil {
		return fmt.Errorf("render password reset: %w", err)
	}
	return s.send(ctx, in.To, subject, text, html, "send password reset")
}

func (s *SESSender) send(ctx context.Context, to, subject, text, html, label string) error {
	_, err := s.client.SendEmail(ctx, &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(s.fromAddress),
		Destination:      &types.Destination{ToAddresses: []string{to}},
		Content: &types.EmailContent{
			Simple: &types.Message{
				Subject: &types.Content{Data: aws.String(subject), Charset: aws.String("UTF-8")},
				Body: &types.Body{
					Text: &types.Content{Data: aws.String(text), Charset: aws.String("UTF-8")},
					Html: &types.Content{Data: aws.String(html), Charset: aws.String("UTF-8")},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}
