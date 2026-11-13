package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/more-than-code/bot-api-server/internal/config"
	"github.com/more-than-code/bot-api-server/internal/memstore"
)

func TestUnknownPollsDoNotGrowTheMap(t *testing.T) {
	ctx := context.Background()
	svc := New(memstore.New(), config.Config{DeviceVerificationURI: "https://console.example/device"}, nil)
	for i := 0; i < 100; i++ {
		code := fmt.Sprintf("unknown-device-code-%d", i)
		if _, err := svc.PollDevice(ctx, code); !errors.Is(err, ErrExpiredToken) {
			t.Fatalf("poll %d: %v", i, err)
		}
	}
	svc.mu.Lock()
	n := len(svc.polls)
	svc.mu.Unlock()
	if n != 0 {
		t.Fatalf("poll map %d", n)
	}
	dc, err := svc.StartDevice(ctx, "cli")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PollDevice(ctx, dc.DeviceCode); !errors.Is(err, ErrPending) {
		t.Fatalf("known poll: %v", err)
	}
	svc.mu.Lock()
	n = len(svc.polls)
	svc.mu.Unlock()
	if n != 1 {
		t.Fatalf("known code did not take one slot: %d", n)
	}
}

func TestPollMapCap(t *testing.T) {
	ctx := context.Background()
	svc := New(memstore.New(), config.Config{DeviceVerificationURI: "https://console.example/device"}, nil)
	for i := 0; i < pollCap; i++ {
		dc, err := svc.StartDevice(ctx, "cli")
		if err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		if _, err := svc.PollDevice(ctx, dc.DeviceCode); !errors.Is(err, ErrPending) {
			t.Fatalf("poll %d: %v", i, err)
		}
	}
	svc.mu.Lock()
	n := len(svc.polls)
	svc.mu.Unlock()
	if n != pollCap {
		t.Fatalf("map %d", n)
	}
	dc, err := svc.StartDevice(ctx, "cli")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PollDevice(ctx, dc.DeviceCode); !errors.Is(err, ErrSlowDown) {
		t.Fatalf("over cap: %v", err)
	}
	if _, err := svc.PollDevice(ctx, "still-unknown"); !errors.Is(err, ErrExpiredToken) {
		t.Fatalf("unknown while full: %v", err)
	}
	svc.mu.Lock()
	n = len(svc.polls)
	svc.mu.Unlock()
	if n != pollCap {
		t.Fatalf("map grew to %d", n)
	}
}

func TestIPLimitsAreSeparate(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	svc := New(memstore.New(), config.Config{}, func() time.Time { return now })
	for i := 0; i < deviceIPLimit; i++ {
		if err := svc.AllowDeviceIP("203.0.113.8"); err != nil {
			t.Fatalf("code %d: %v", i, err)
		}
	}
	if err := svc.AllowDeviceIP("203.0.113.8"); !errors.Is(err, ErrIPLimited) {
		t.Fatalf("code over: %v", err)
	}
	for i := 0; i < tokenIPLimit; i++ {
		if err := svc.AllowDeviceTokenIP("203.0.113.8"); err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
	}
	if err := svc.AllowDeviceTokenIP("203.0.113.8"); !errors.Is(err, ErrIPLimited) {
		t.Fatalf("token over: %v", err)
	}
	if err := svc.AllowGoogleIP("203.0.113.8"); err != nil {
		t.Fatalf("google: %v", err)
	}
}

func TestNormalizeAuth(t *testing.T) {
	str := func(s string) *string { return &s }
	cases := []struct {
		name string
		in   *string
		want string
		ok   bool
	}{
		{"absent", nil, "none", true},
		{"empty", str(""), "none", true},
		{"none", str("none"), "none", true},
		{"client_secret_post", str("client_secret_post"), "none", true},
		{"client_secret_basic", str("client_secret_basic"), "none", true},
		{"private_key_jwt", str("private_key_jwt"), "", false},
		{"unknown", str("made_up"), "", false},
	}
	for _, tc := range cases {
		got, ok := normalizeAuth(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: got %q %v, want %q %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestRefusedAuthLabel(t *testing.T) {
	cases := map[string]string{
		"client_secret_jwt":           "client_secret_jwt",
		"private_key_jwt":             "private_key_jwt",
		"tls_client_auth":             "tls_client_auth",
		"self_signed_tls_client_auth": "self_signed_tls_client_auth",
		"made_up":                     "other",
		"https://leak.example/cb":     "other",
		"CLIENT_SECRET_POST":          "other",
		"Private_Key_JWT":             "other",
		" private_key_jwt":            "other",
	}
	for in, want := range cases {
		if got := refusedAuthLabel(in); got != want {
			t.Errorf("refusedAuthLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
