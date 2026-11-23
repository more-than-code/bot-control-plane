package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// api-v1 v1.50 item 4.
func TestHealthURLs(t *testing.T) {
	for _, raw := range []string{
		"", "https://127.0.0.1:8090/", "http://example.com:8090/", "http://10.0.0.1:8090/",
		"http://[::1]:8090/", "http://127.0.0.1/", "http://localhost/", "http://127.0.0.1:0/",
		"http://127.0.0.1:65536/", "http://127.0.0.1:08090/", "http://127.0.0.1:x/",
		"http://user:pw@127.0.0.1:8090/", "http://127.0.0.1:8090", "http://127.0.0.1:8090/#f",
		"http://127.0.0.2:8090/", "127.0.0.1:8090/",
	} {
		var stderr bytes.Buffer
		if code := health([]string{raw}, &stderr); code != 2 {
			t.Fatalf("%q: exit %d", raw, code)
		}
	}
	for _, args := range [][]string{nil, {"http://127.0.0.1:8090/", "x"}} {
		if code := health(args, &bytes.Buffer{}); code != 2 {
			t.Fatalf("%v: exit %d", args, code)
		}
	}
	for raw, want := range map[string]string{
		"http://127.0.0.1:8090/healthz": "http://127.0.0.1:8090/healthz",
		"http://localhost:5180/":        "http://127.0.0.1:5180/",
		"http://127.0.0.1:1/a?b=c":      "http://127.0.0.1:1/a?b=c",
	} {
		if got, ok := loopbackURL(raw); !ok || got != want {
			t.Fatalf("%q: %q %v", raw, got, ok)
		}
	}
}

func TestHealthAnswers(t *testing.T) {
	healthTimeout = 300 * time.Millisecond
	t.Cleanup(func() { healthTimeout = 3 * time.Second })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusNoContent)
		case "/fail":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/redirect":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/slow":
			time.Sleep(time.Second)
		}
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	for path, want := range map[string]int{"/ok": 0, "/fail": 1, "/redirect": 1, "/slow": 1} {
		for _, host := range []string{"127.0.0.1", "localhost"} {
			if host == "localhost" && path != "/ok" {
				continue
			}
			var stderr bytes.Buffer
			if code := health([]string{"http://" + host + ":" + port + path}, &stderr); code != want {
				t.Fatalf("%s%s: exit %d (%s)", host, path, code, stderr.String())
			}
		}
	}
	// Nothing listening.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, closed, _ := net.SplitHostPort(l.Addr().String())
	l.Close()
	if code := health([]string{"http://127.0.0.1:" + closed + "/"}, &bytes.Buffer{}); code != 1 {
		t.Fatalf("closed port: exit %d", code)
	}
}
