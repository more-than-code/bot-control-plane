package catalog

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
)

// Generate kinds and limits (api-v1 v1.27 item 1).
const (
	GenPassword  = "password"
	GenHex       = "hex"
	GenBase64URL = "base64url"

	genPasswordDefault = 32
	genPasswordMin     = 16
	genPasswordMax     = 64
	genRandomBytes     = 32
)

const passwordAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// passwordCutoff is the largest multiple of len(passwordAlphabet) that fits
// in a byte (62 * 4 = 248). Bytes at or above it are rejected, so every
// character is equally likely.
const passwordCutoff = 256 - 256%len(passwordAlphabet)

// Generate is the v1.27 generate object of an env key PUT.
type Generate struct {
	Kind   string `json:"kind"`
	Length *int   `json:"length"`
}

// Validate checks the kind and the length rule. Messages name no value.
func (g Generate) Validate() error {
	switch g.Kind {
	case GenPassword:
		if g.Length != nil && (*g.Length < genPasswordMin || *g.Length > genPasswordMax) {
			return fieldErr("generate.length must be 16 to 64")
		}
	case GenHex, GenBase64URL:
		if g.Length != nil {
			return fieldErr("generate.length is only for kind password")
		}
	default:
		return fieldErr(`generate.kind must be "password", "hex" or "base64url"`)
	}
	return nil
}

// Chars is the length the plan names: the password length, or 0 for the
// fixed-size kinds.
func (g Generate) Chars() int {
	if g.Kind != GenPassword {
		return 0
	}
	if g.Length == nil {
		return genPasswordDefault
	}
	return *g.Length
}

// NewValue validates g and makes a value from crypto/rand.
func (g Generate) NewValue() (string, error) {
	return g.newValue(rand.Reader)
}

func (g Generate) newValue(src io.Reader) (string, error) {
	if err := g.Validate(); err != nil {
		return "", err
	}
	switch g.Kind {
	case GenPassword:
		return randomPassword(src, g.Chars())
	case GenHex:
		buf, err := randomBytes(src, genRandomBytes)
		if err != nil {
			return "", err
		}
		return hex.EncodeToString(buf), nil
	default:
		buf, err := randomBytes(src, genRandomBytes)
		if err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(buf), nil
	}
}

var errRandom = errors.New("random source failed")

func randomBytes(src io.Reader, n int) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(src, buf); err != nil {
		return nil, errRandom
	}
	return buf, nil
}

// randomPassword draws n characters from passwordAlphabet by rejection
// sampling, so there is no modulo bias.
func randomPassword(src io.Reader, n int) (string, error) {
	out := make([]byte, 0, n)
	buf := make([]byte, n)
	for len(out) < n {
		if _, err := io.ReadFull(src, buf); err != nil {
			return "", errRandom
		}
		for _, b := range buf {
			if int(b) >= passwordCutoff {
				continue
			}
			out = append(out, passwordAlphabet[int(b)%len(passwordAlphabet)])
			if len(out) == n {
				break
			}
		}
	}
	return string(out), nil
}
