package auth

import (
	"errors"

	"golang.org/x/crypto/bcrypt"
)

const minPasswordLen = 8

var ErrPasswordTooShort = errors.New("password must be at least 8 characters")

func HashPassword(plain string) (string, error) {
	if len(plain) < minPasswordLen {
		return "", ErrPasswordTooShort
	}
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

func VerifyPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}
