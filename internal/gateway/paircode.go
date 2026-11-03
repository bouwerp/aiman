package gateway

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	// PairCodesFileName holds unredeemed pairing codes as sha256 hashes.
	PairCodesFileName = "gateway-pair-codes.json"
	// PairCodeTTL is how long a minted code may be redeemed.
	PairCodeTTL = 5 * time.Minute
	pairCodeLen = 10
)

type pairCode struct {
	Hash      string    `json:"sha256"`
	Name      string    `json:"name,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}

type pairCodesFile struct {
	Codes []pairCode `json:"codes"`
}

// PairCodes is the file of pending pairing codes. The CLI mints into it and
// the service consumes from it, so every change is a locked
// read-modify-write and the file is reread each time.
type PairCodes struct {
	path string
	now  func() time.Time
}

// NewPairCodes returns the code file at path.
func NewPairCodes(path string) *PairCodes {
	return &PairCodes{path: path, now: time.Now}
}

// Mint creates a single-use code valid for PairCodeTTL. name is the device
// name used when the redeeming request does not give one.
func (p *PairCodes) Mint(name string) (string, time.Time, error) {
	code, err := newPairCode()
	if err != nil {
		return "", time.Time{}, err
	}
	expires := p.now().Add(PairCodeTTL)
	err = withFileLock(p.path, func() error {
		codes, err := p.load()
		if err != nil {
			return err
		}
		codes = append(p.live(codes), pairCode{Hash: hashHex(code), Name: strings.TrimSpace(name), ExpiresAt: expires})
		return p.save(codes)
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return code, expires, nil
}

// Consume redeems code. ok is false for an unknown, used or expired code.
func (p *PairCodes) Consume(code string) (name string, ok bool, err error) {
	code = normalizePairCode(code)
	if len(code) != pairCodeLen {
		return "", false, nil
	}
	want := []byte(hashHex(code))
	err = withFileLock(p.path, func() error {
		codes, err := p.load()
		if err != nil {
			return err
		}
		live := p.live(codes)
		kept := live[:0]
		for _, c := range live {
			if !ok && subtle.ConstantTimeCompare([]byte(c.Hash), want) == 1 {
				ok, name = true, c.Name
				continue
			}
			kept = append(kept, c)
		}
		if !ok && len(kept) == len(codes) {
			return nil
		}
		return p.save(kept)
	})
	if err != nil {
		return "", false, err
	}
	return name, ok, nil
}

func (p *PairCodes) live(codes []pairCode) []pairCode {
	now := p.now()
	out := make([]pairCode, 0, len(codes))
	for _, c := range codes {
		if now.Before(c.ExpiresAt) {
			out = append(out, c)
		}
	}
	return out
}

func (p *PairCodes) load() ([]pairCode, error) {
	data, err := os.ReadFile(p.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f pairCodesFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("gateway pair codes: %w", err)
	}
	return f.Codes, nil
}

func (p *PairCodes) save(codes []pairCode) error {
	if codes == nil {
		codes = []pairCode{}
	}
	body, err := json.MarshalIndent(pairCodesFile{Codes: codes}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(p.path, append(body, '\n'))
}

func newPairCode() (string, error) {
	// 7 bytes give 56 bits, of which the first 50 make the 10 characters.
	raw := make([]byte, 7)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating pairing code: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)[:pairCodeLen], nil
}

// normalizePairCode accepts a code typed with lower case, spaces or dashes.
func normalizePairCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(code) {
		if r == ' ' || r == '-' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
