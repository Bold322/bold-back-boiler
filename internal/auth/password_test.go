package auth_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Bold322/bold-back-boiler/internal/auth"
)

func TestHashPassword_ProducesNonEmptyDifferentHashEachCall(t *testing.T) {
	h1, err := auth.HashPassword("hunter2!")
	require.NoError(t, err)
	require.NotEmpty(t, h1)

	h2, err := auth.HashPassword("hunter2!")
	require.NoError(t, err)
	require.NotEqual(t, h1, h2, "bcrypt should use a fresh salt per call")
}

func TestVerifyPassword_ReturnsTrueForCorrectPassword(t *testing.T) {
	hash, err := auth.HashPassword("correct horse battery staple")
	require.NoError(t, err)

	require.True(t, auth.VerifyPassword(hash, "correct horse battery staple"))
}

func TestVerifyPassword_ReturnsFalseForWrongPassword(t *testing.T) {
	hash, err := auth.HashPassword("rightpassword")
	require.NoError(t, err)

	require.False(t, auth.VerifyPassword(hash, "wrong"))
}

func TestHashPassword_ReturnsErrorWhenPasswordTooShort(t *testing.T) {
	_, err := auth.HashPassword("short")
	require.Error(t, err)
}

func TestVerifyPassword_ReturnsFalseForGarbageHash(t *testing.T) {
	require.False(t, auth.VerifyPassword("not-a-valid-bcrypt-hash", "anything"))
}
