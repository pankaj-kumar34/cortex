package auth

import (
	"crypto/subtle"
	"errors"
)

var (
	// ErrInvalidToken is returned when token validation fails
	ErrInvalidToken = errors.New("invalid authentication token")
)

// TokenValidator handles token-based authentication
type TokenValidator struct {
	sharedToken string
}

// NewTokenValidator creates a new token validator with the shared token
func NewTokenValidator(sharedToken string) *TokenValidator {
	return &TokenValidator{
		sharedToken: sharedToken,
	}
}

// Validate checks if the provided token matches the shared token
// Uses constant-time comparison to prevent timing attacks
func (tv *TokenValidator) Validate(token string) error {
	if subtle.ConstantTimeCompare([]byte(tv.sharedToken), []byte(token)) != 1 {
		return ErrInvalidToken
	}
	return nil
}

// GetToken returns the shared token (for workers to use)
func (tv *TokenValidator) GetToken() string {
	return tv.sharedToken
}

// Made with Bob
