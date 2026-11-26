// Package agentrelease finds the newest published bot-agent release
// (api-v1 v1.62, spec docs/spec-agent-latest-version.md §4.1): it asks
// GitHub which release is latest, then accepts its version only when the
// release's SHA256SUMS.sig verifies under a key the hosts' helper trusts
// and SHA256SUMS names that version's binaries.
//
// No error, log line or return value carries a GitHub response body.
package agentrelease

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

// Production addresses. Tests point a Checker at a fake.
const (
	// LatestURL is GitHub's documented "latest release" API, which never
	// returns a draft or a prerelease.
	LatestURL = "https://api.github.com/repos/more-than-code/bot-agent/releases/latest"
	// DownloadBase is the helper's fixed release address (bot-agent
	// internal/upgrade.BaseURL).
	DownloadBase = "https://github.com/more-than-code/bot-agent/releases/download/"
)

const (
	requestTimeout = 10 * time.Second
	maxRedirects   = 5
	maxRelease     = 1 << 20
	maxSums        = 4 << 10
	// maxSig is bot-agent internal/release.MaxSigFile.
	maxSig    = 128
	userAgent = "deploybot"
)

// PublicKeys is a copy of bot-agent internal/release.PublicKeys, in the same
// order: a release is accepted when its signature verifies under any of
// them. A change there needs the same change here, or the API stops
// offering releases signed only by the new key.
//
//   - Joe's offline release key (2026-11-23), the recovery key.
//   - The CI release key, an AWS KMS Ed25519 key in ap-southeast-2
//     (2026-11-25), used only by bot-agent's release workflow.
var PublicKeys = []ed25519.PublicKey{
	mustKey("Ee1UIjnw5zgPNw5ZtKNBTCdLvYM3ozEcSRcfWZaCHh8="),
	mustKey("DCG1ElztSCaavg19pGaC2Hud1CoLZXvIDrlFr0RXBPA="),
}

func mustKey(b64 string) ed25519.PublicKey {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		panic("agentrelease: bad compiled public key")
	}
	return ed25519.PublicKey(raw)
}

var (
	// ErrUnavailable is any failed request: network, status, size, redirect.
	ErrUnavailable = errors.New("bot-agent release lookup failed")
	// ErrBadRelease is a latest release whose tag is not vX.Y.Z, or whose
	// SHA256SUMS does not name that version's binaries.
	ErrBadRelease = errors.New("bot-agent latest release is not usable")
	// ErrBadSignature is a SHA256SUMS.sig that is malformed or verifies
	// under no trusted key.
	ErrBadSignature = errors.New("bot-agent release signature does not verify")
)

var tagRe = regexp.MustCompile(`^v([0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4})$`)

// allowedHosts are the hosts a request or redirect may reach: the helper's
// list (bot-agent internal/upgrade) plus the GitHub API.
var allowedHosts = map[string]bool{
	"github.com":                           true,
	"api.github.com":                       true,
	"objects.githubusercontent.com":        true,
	"release-assets.githubusercontent.com": true,
}

// Checker looks up the newest bot-agent release. The zero value is not
// usable; New gives the production one.
type Checker struct {
	LatestURL    string
	DownloadBase string
	HTTP         *http.Client
	Keys         []ed25519.PublicKey
}

// New is the production Checker: GitHub over HTTPS, redirects only to the
// allowed hosts, 10 s per request.
func New() *Checker {
	return &Checker{
		LatestURL:    LatestURL,
		DownloadBase: DownloadBase,
		HTTP:         &http.Client{Timeout: requestTimeout, CheckRedirect: checkRedirect},
		Keys:         PublicKeys,
	}
}

func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return ErrUnavailable
	}
	if !allowedURL(req.URL) {
		return ErrUnavailable
	}
	return nil
}

func allowedURL(u *url.URL) bool {
	return u.Scheme == "https" && u.User == nil && u.Port() == "" && allowedHosts[u.Hostname()]
}

// Check returns the version (X.Y.Z) of the latest bot-agent release when
// its signature verifies, or one of the package errors.
func (c *Checker) Check(ctx context.Context) (string, error) {
	raw, err := c.get(ctx, c.LatestURL, maxRelease, "application/vnd.github+json")
	if err != nil {
		return "", err
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if json.Unmarshal(raw, &rel) != nil {
		return "", ErrBadRelease
	}
	m := tagRe.FindStringSubmatch(rel.TagName)
	if m == nil {
		return "", ErrBadRelease
	}
	version := m[1]
	base := c.DownloadBase + "v" + version + "/"
	sums, err := c.get(ctx, base+"SHA256SUMS", maxSums, "")
	if err != nil {
		return "", err
	}
	sig, err := c.get(ctx, base+"SHA256SUMS.sig", maxSig, "")
	if err != nil {
		return "", err
	}
	if err := Verify(c.Keys, sums, sig); err != nil {
		return "", err
	}
	if !NamesVersion(sums, version) {
		return "", ErrBadRelease
	}
	return version, nil
}

// get fetches rawURL and returns its body when the answer is 200 and the
// body is at most max bytes.
func (c *Checker) get(ctx context.Context, rawURL string, max int64, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	req.Header.Set("User-Agent", userAgent)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, ErrUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil || int64(len(body)) > max {
		return nil, ErrUnavailable
	}
	return body, nil
}

// Verify checks sigFile (one base64 line of a 64-byte Ed25519 signature,
// optional trailing newline) over the exact sums bytes under any of keys.
// As the helper's VerifyAny does, an empty list, or a list holding the
// all-zero placeholder or a key of the wrong size, verifies nothing.
func Verify(keys []ed25519.PublicKey, sums, sigFile []byte) error {
	line := bytes.TrimSuffix(sigFile, []byte("\n"))
	line = bytes.TrimSuffix(line, []byte("\r"))
	if len(line) == 0 || bytes.ContainsAny(line, "\r\n \t") {
		return ErrBadSignature
	}
	sig, err := base64.StdEncoding.Strict().DecodeString(string(line))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return ErrBadSignature
	}
	zero := make([]byte, ed25519.PublicKeySize)
	for _, k := range keys {
		if len(k) != ed25519.PublicKeySize || bytes.Equal(k, zero) {
			return ErrBadSignature
		}
	}
	for _, k := range keys {
		if ed25519.Verify(k, sums, sig) {
			return nil
		}
	}
	return ErrBadSignature
}

// NamesVersion reports whether sums has the lines for
// bot-agent-v<version>-linux-amd64 and -linux-arm64 (64 lowercase hex
// characters, two spaces, the name). It binds a signed SHA256SUMS to its
// tag, and to a release that serves both host architectures.
func NamesVersion(sums []byte, version string) bool {
	for _, arch := range []string{"amd64", "arm64"} {
		re := regexp.MustCompile(fmt.Sprintf(`(?m)^[0-9a-f]{64}  %s$`, regexp.QuoteMeta("bot-agent-v"+version+"-linux-"+arch)))
		if !re.Match(sums) {
			return false
		}
	}
	return true
}
