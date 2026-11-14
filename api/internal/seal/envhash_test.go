package seal

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"testing"
)

// manualEnvHash computes the v1.14 hash with RFC 5869 HKDF written out by
// hand (zero salt, one expand block), as an independent check.
func manualEnvHash(secretsKey []byte, lines string) string {
	ext := hmac.New(sha256.New, make([]byte, sha256.Size))
	ext.Write(secretsKey)
	prk := ext.Sum(nil)
	exp := hmac.New(sha256.New, prk)
	exp.Write([]byte("deploybot env hash v1"))
	exp.Write([]byte{1})
	okm := exp.Sum(nil)
	mac := hmac.New(sha256.New, okm)
	mac.Write([]byte(lines))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestEnvHash(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	box, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"B": "two", "A": "one", "A_B": "x=y"}
	got, err := box.EnvHash(env)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(got) {
		t.Fatalf("hash %q", got)
	}
	if want := manualEnvHash(key, "A=one\nA_B=x=y\nB=two\n"); got != want {
		t.Fatalf("hash %s want %s", got, want)
	}
	// Map order never matters.
	for i := 0; i < 20; i++ {
		again, _ := box.EnvHash(map[string]string{"A_B": "x=y", "A": "one", "B": "two"})
		if again != got {
			t.Fatal("hash depends on map order")
		}
	}
	changed, _ := box.EnvHash(map[string]string{"B": "two", "A": "one!", "A_B": "x=y"})
	if changed == got {
		t.Fatal("value change kept the hash")
	}
	other, _ := New(bytes.Repeat([]byte{8}, 32))
	keyed, _ := other.EnvHash(env)
	if keyed == got {
		t.Fatal("hash is not keyed")
	}
	// A plain SHA-256 of the lines is not the hash.
	plain := sha256.Sum256([]byte("A=one\nA_B=x=y\nB=two\n"))
	if hex.EncodeToString(plain[:]) == got {
		t.Fatal("hash is unkeyed")
	}
	empty, err := box.EnvHash(nil)
	if err != nil || empty != manualEnvHash(key, "") {
		t.Fatalf("empty env %s %v", empty, err)
	}
	var none *Box
	if _, err := none.EnvHash(env); err == nil {
		t.Fatal("nil box hashed")
	}
}
