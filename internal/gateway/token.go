package gateway

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	TokenFileName = "gateway-token"
	tokenBytes    = 32
)

// LoadOrCreateToken returns the bearer token at path, creating a 0600 file
// of random hex if it does not exist.
func LoadOrCreateToken(path string) (token string, created bool, err error) {
	data, err := os.ReadFile(path)
	if err == nil {
		tok := strings.TrimSpace(string(data))
		if tok == "" {
			return "", false, fmt.Errorf("gateway token file %s is empty", path)
		}
		return tok, false, nil
	}
	if !os.IsNotExist(err) {
		return "", false, err
	}
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", false, fmt.Errorf("generating gateway token: %w", err)
	}
	tok := hex.EncodeToString(raw)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", false, err
	}
	return tok, true, nil
}

// TokenEqual is a constant-time compare of two bearer tokens.
func TokenEqual(got, want string) bool {
	if len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
