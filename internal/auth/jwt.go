package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Claims is the verified token shape carried through middleware. UserType
// drives management-route gating. LevelIDs drives document read access.
type Claims struct {
	UserID    uuid.UUID
	TenantID  uuid.UUID
	UserType  string
	LevelIDs  []uuid.UUID
	SessionID uuid.UUID
}

// IsAdmin returns true for tenant admins and global superadmins. Used by
// RequireAdmin and uploader-subset checks. Does not gate document reads.
func (c *Claims) IsAdmin() bool {
	return c.UserType == "admin" || c.UserType == "superadmin"
}

// IsSuperadmin gates the /superadmin/* routes.
func (c *Claims) IsSuperadmin() bool {
	return c.UserType == "superadmin"
}

type JWTSigner struct {
	secret     []byte
	issuer     string
	ttlSeconds int
}

func NewJWTSigner(secret, issuer string, ttlSeconds int) *JWTSigner {
	return &JWTSigner{
		secret:     []byte(secret),
		issuer:     issuer,
		ttlSeconds: ttlSeconds,
	}
}

type internalClaims struct {
	TenantID  string   `json:"tenant_id"`
	UserType  string   `json:"user_type"`
	LevelIDs  []string `json:"level_ids"`
	SessionID string   `json:"sid,omitempty"`
	jwt.RegisteredClaims
}

func (s *JWTSigner) Sign(userID, tenantID uuid.UUID, userType string, levelIDs []uuid.UUID) (string, error) {
	return s.SignWithSession(userID, tenantID, userType, levelIDs, uuid.Nil)
}

// SignWithSession issues an access token bound to a refresh-token family via
// the sid claim. sessionID == uuid.Nil emits no sid (used only by the
// transitional Sign wrapper).
func (s *JWTSigner) SignWithSession(userID, tenantID uuid.UUID, userType string, levelIDs []uuid.UUID, sessionID uuid.UUID) (string, error) {
	encoded := make([]string, len(levelIDs))
	for i, id := range levelIDs {
		encoded[i] = id.String()
	}
	sid := ""
	if sessionID != uuid.Nil {
		sid = sessionID.String()
	}
	now := time.Now()
	claims := internalClaims{
		TenantID:  tenantID.String(),
		UserType:  userType,
		LevelIDs:  encoded,
		SessionID: sid,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.issuer,
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(s.ttlSeconds) * time.Second)),
			NotBefore: jwt.NewNumericDate(now),
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := t.SignedString(s.secret)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return signed, nil
}

func (s *JWTSigner) Verify(token string) (*Claims, error) {
	parsed, err := jwt.ParseWithClaims(token, &internalClaims{}, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, errors.New("unexpected signing method")
		}
		return s.secret, nil
	}, jwt.WithIssuer(s.issuer))
	if err != nil {
		return nil, fmt.Errorf("verify token: %w", err)
	}
	c, ok := parsed.Claims.(*internalClaims)
	if !ok || !parsed.Valid {
		return nil, errors.New("invalid claims")
	}
	uid, err := uuid.Parse(c.Subject)
	if err != nil {
		return nil, fmt.Errorf("parse subject uuid: %w", err)
	}
	tid, err := uuid.Parse(c.TenantID)
	if err != nil {
		return nil, fmt.Errorf("parse tenant uuid: %w", err)
	}
	switch c.UserType {
	case "admin", "worker", "superadmin":
	default:
		return nil, fmt.Errorf("invalid user_type %q", c.UserType)
	}
	levelIDs := make([]uuid.UUID, 0, len(c.LevelIDs))
	for _, raw := range c.LevelIDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("parse level id %q: %w", raw, err)
		}
		levelIDs = append(levelIDs, id)
	}
	var sessionID uuid.UUID
	if c.SessionID != "" {
		sessionID, err = uuid.Parse(c.SessionID)
		if err != nil {
			return nil, fmt.Errorf("parse session id %q: %w", c.SessionID, err)
		}
	}
	return &Claims{UserID: uid, TenantID: tid, UserType: c.UserType, LevelIDs: levelIDs, SessionID: sessionID}, nil
}
