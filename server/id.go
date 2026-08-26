package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"math/big"
	"strings"
)

// idAlphabet is intentionally [A-Za-z0-9] -> 62^8 = 2.18e14 possibilities.
const idAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

const idLength = 8

// NewID returns a cryptographically random 8 character slug. Rejection-free
// generation via crypto/rand.Int keeps the distribution perfectly uniform.
func NewID() (string, error) {
	var sb strings.Builder
	sb.Grow(idLength)
	max := big.NewInt(int64(len(idAlphabet)))
	for i := 0; i < idLength; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		sb.WriteByte(idAlphabet[n.Int64()])
	}
	return sb.String(), nil
}

// ValidID guards every path parameter before it ever reaches the object store.
func ValidID(s string) bool {
	if len(s) != idLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}

// NewDeleteToken returns the secret that lets an uploader remove their own file.
func NewDeleteToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
