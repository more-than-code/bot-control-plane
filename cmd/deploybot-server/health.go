package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// healthTimeout is the whole check's budget (api-v1 v1.50 item 4).
var healthTimeout = 3 * time.Second

// health is `deploybot-server health <url>`, the image's Docker
// healthcheck (the distroless base has no curl). It exits 0 when a GET
// answers 2xx within healthTimeout and 1 otherwise. Only
// http://127.0.0.1:<port>/… and http://localhost:<port>/… are accepted;
// anything else exits 2. Redirects are not followed and proxy settings are
// ignored, so the check never leaves the loopback address it names.
func health(args []string, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	target, ok := loopbackURL(args[0])
	if !ok {
		fmt.Fprintln(stderr, "health: the URL must be http://127.0.0.1:<port>/… or http://localhost:<port>/…")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), healthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		fmt.Fprintln(stderr, "health: bad request")
		return 2
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	res, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(stderr, "health: no answer")
		return 1
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		fmt.Fprintf(stderr, "health: status %d\n", res.StatusCode)
		return 1
	}
	return 0
}

// loopbackURL accepts http on 127.0.0.1 or localhost with an explicit
// port 1–65535 and a path starting with "/", and no user info or fragment.
// The URL it returns always names 127.0.0.1.
func loopbackURL(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return "", false
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || (host != "127.0.0.1" && host != "localhost") {
		return "", false
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return "", false
	}
	if u.Path == "" || u.Path[0] != '/' {
		return "", false
	}
	// localhost is dialled as 127.0.0.1, so a resolver or hosts file cannot
	// send the check elsewhere (api-v1 v1.50 item 9).
	u.Host = net.JoinHostPort("127.0.0.1", port)
	return u.String(), true
}
