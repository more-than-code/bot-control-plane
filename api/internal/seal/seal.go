// Package seal encrypts env values, registry tokens, and secret project
// files at rest, and derives the keyed env hash of a resolved env
// (agent-protocol v1.14). Plaintext, keys, and nonces are never logged.
package seal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sort"
	"strconv"
)

// envHashInfo is the HKDF info of the env hash key (agent-protocol v1.14).
const envHashInfo = "deploybot env hash v1"

// KeyVersion is the only key version this process writes.
const KeyVersion = 1

// Box is an AES-256-GCM key. A nil Box means the key is not configured.
type Box struct {
	key []byte
}

// New rejects a key that is not 32 bytes.
func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, errors.New("invalid secrets key")
	}
	cp := make([]byte, 32)
	copy(cp, key)
	return &Box{key: cp}, nil
}

// EnvAAD binds a ciphertext to one env value.
func EnvAAD(projectID, setName, key string) string {
	return projectID + "|" + setName + "|" + key
}

// RegistryAAD binds a ciphertext to one registry host.
func RegistryAAD(projectID, host string) string {
	return projectID + "|registry|" + host
}

// RegistryRefreshAAD binds a GitHub refresh token to one registry host
// (api-v1 v1.43). It differs from RegistryAAD, so the access token and the
// refresh token ciphertexts cannot be swapped.
func RegistryRefreshAAD(projectID, host string) string {
	return "registry-refresh|" + projectID + "|" + host
}

// FileAAD binds a ciphertext to one project file.
func FileAAD(projectID, name string) string {
	return projectID + "|file|" + name
}

// HostFileAAD binds a ciphertext to one version of one host config file
// (api-v1 v1.44).
func HostFileAAD(hostID, name string, version int) string {
	return "host-file|" + hostID + "|" + name + "|" + strconv.Itoa(version)
}

// Seal returns a fresh 12-byte nonce and the ciphertext. The nonce is not
// inside the ciphertext.
func (b *Box) Seal(aad string, plain []byte) (nonce, ciphertext []byte, err error) {
	if b == nil {
		return nil, nil, errors.New("secrets key not configured")
	}
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, nil, errors.New("encrypt failed")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, errors.New("encrypt failed")
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, errors.New("encrypt failed")
	}
	ciphertext = gcm.Seal(nil, nonce, plain, []byte(aad))
	return nonce, ciphertext, nil
}

// Open fails closed. The error does not include plaintext or the AAD.
func (b *Box) Open(aad string, nonce, ciphertext []byte) ([]byte, error) {
	if b == nil {
		return nil, errors.New("secrets key not configured")
	}
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, errors.New("decrypt failed")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("decrypt failed")
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, errors.New("decrypt failed")
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, []byte(aad))
	if err != nil {
		return nil, errors.New("decrypt failed")
	}
	return plain, nil
}

// EnvHash is the agent-protocol v1.14 env_hash of a resolved env: lowercase
// hex HMAC-SHA256 over "KEY=value\n" lines in byte order of KEY, keyed by
// HKDF-SHA256(secrets key, no salt, info envHashInfo). The key is derived
// from the secrets key, so the hash cannot be checked offline against a
// guessed value.
func (b *Box) EnvHash(env map[string]string) (string, error) {
	if b == nil {
		return "", errors.New("secrets key not configured")
	}
	key, err := hkdf.Key(sha256.New, b.key, nil, envHashInfo, sha256.Size)
	if err != nil {
		return "", errors.New("env hash failed")
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	mac := hmac.New(sha256.New, key)
	for _, k := range keys {
		mac.Write([]byte(k))
		mac.Write([]byte{'='})
		mac.Write([]byte(env[k]))
		mac.Write([]byte{'\n'})
	}
	return hex.EncodeToString(mac.Sum(nil)), nil
}
