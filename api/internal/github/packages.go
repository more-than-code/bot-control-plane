package github

// Container package versions on ghcr.io through the GitHub REST API (api-v1
// v1.45): the owner kind, the version list, one version, and the delete of
// one version by id. Every call uses the project's Connect GitHub token,
// which the caller opens. No GitHub body is returned, logged or echoed.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DeleteScope is the scope a Connect GitHub login needs to delete package
// versions (api-v1 v1.45). It is asked for only on the opt-in step.
const DeleteScope = "delete:packages"

const (
	// versionsPerPage and versionsMaxPages bound one version list.
	versionsPerPage  = 100
	versionsMaxPages = 5
	// listBodyMax is the body cap of one list page: 100 versions with
	// their tags fit well inside it.
	listBodyMax = 1 << 20
)

var (
	// ErrNotFound is a 404 from GitHub: no such package, version or owner
	// visible to the token.
	ErrNotFound = errors.New("github package not found")
	// ErrDeleteRefused is a 403 (or 400/422) on a delete: the user is not an
	// admin of the package, the org restricts the OAuth app, or GitHub refuses
	// to delete the last version of a popular package.
	ErrDeleteRefused = errors.New("github refused the delete")

	digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	// packageTagRe is the v1.16 tag rule; other tags are dropped.
	packageTagRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
)

// Package names one container package: its owner, whether the owner is an
// organization, and the package name (the repository path after the owner).
type Package struct {
	Owner string
	Org   bool
	Name  string
}

// Version is one container package version.
type Version struct {
	ID        int64
	Digest    string
	Tags      []string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// HasDeleteScope reports whether scopes holds DeleteScope.
func HasDeleteScope(scopes []string) bool {
	for _, s := range scopes {
		if s == DeleteScope {
			return true
		}
	}
	return false
}

// SplitGHCRPath splits a ghcr.io repository path into owner and package
// name ("acme/api/worker" → "acme", "api/worker"). ok is false when the path
// has no package part or the owner is not a GitHub login.
func SplitGHCRPath(path string) (owner, name string, ok bool) {
	owner, name, found := strings.Cut(path, "/")
	if !found || name == "" || !loginRe.MatchString(owner) {
		return "", "", false
	}
	return owner, name, true
}

// OwnerIsOrg reads GET /users/{owner} and reports whether it is an
// organization.
func (c *Client) OwnerIsOrg(ctx context.Context, token, owner string) (bool, error) {
	if !loginRe.MatchString(owner) {
		return false, ErrNotFound
	}
	body, status, err := c.apiCall(ctx, http.MethodGet, token, "/users/"+url.PathEscape(owner), bodyMax)
	if err != nil {
		return false, err
	}
	if status == http.StatusNotFound {
		return false, ErrNotFound
	}
	if status != http.StatusOK {
		return false, ErrFailed
	}
	var u struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(body, &u) != nil {
		return false, ErrFailed
	}
	return u.Type == "Organization", nil
}

// Versions lists the package's versions, newest first as GitHub returns
// them, reading at most versionsMaxPages pages. truncated is true when a
// further page was left unread.
func (c *Client) Versions(ctx context.Context, token string, pkg Package) (out []Version, truncated bool, err error) {
	for page := 1; page <= versionsMaxPages; page++ {
		q := url.Values{}
		q.Set("per_page", strconv.Itoa(versionsPerPage))
		q.Set("page", strconv.Itoa(page))
		body, status, err := c.apiCall(ctx, http.MethodGet, token, pkg.versionsPath()+"?"+q.Encode(), listBodyMax)
		if err != nil {
			return nil, false, err
		}
		if status == http.StatusNotFound {
			return nil, false, ErrNotFound
		}
		if status != http.StatusOK {
			return nil, false, ErrFailed
		}
		var raw []rawVersion
		if json.Unmarshal(body, &raw) != nil {
			return nil, false, ErrFailed
		}
		for _, v := range raw {
			if item, ok := v.version(); ok {
				out = append(out, item)
			}
		}
		if len(raw) < versionsPerPage {
			return out, false, nil
		}
	}
	return out, true, nil
}

// Version reads one version by id.
func (c *Client) Version(ctx context.Context, token string, pkg Package, id int64) (Version, error) {
	body, status, err := c.apiCall(ctx, http.MethodGet, token, pkg.versionsPath()+"/"+strconv.FormatInt(id, 10), bodyMax)
	if err != nil {
		return Version{}, err
	}
	if status == http.StatusNotFound {
		return Version{}, ErrNotFound
	}
	if status != http.StatusOK {
		return Version{}, ErrFailed
	}
	var raw rawVersion
	if json.Unmarshal(body, &raw) != nil {
		return Version{}, ErrFailed
	}
	v, ok := raw.version()
	if !ok || v.ID != id {
		return Version{}, ErrFailed
	}
	return v, nil
}

// DeleteVersion deletes one version by id. Only 204 is a delete.
func (c *Client) DeleteVersion(ctx context.Context, token string, pkg Package, id int64) error {
	_, status, err := c.apiCall(ctx, http.MethodDelete, token, pkg.versionsPath()+"/"+strconv.FormatInt(id, 10), bodyMax)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusNoContent:
		return nil
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusForbidden, http.StatusBadRequest, http.StatusUnprocessableEntity:
		return ErrDeleteRefused
	default:
		return ErrFailed
	}
}

func (p Package) versionsPath() string {
	kind := "users"
	if p.Org {
		kind = "orgs"
	}
	return "/" + kind + "/" + url.PathEscape(p.Owner) + "/packages/container/" + url.PathEscape(p.Name) + "/versions"
}

type rawVersion struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Metadata  struct {
		Container struct {
			Tags []string `json:"tags"`
		} `json:"container"`
	} `json:"metadata"`
}

// version keeps a version with a positive id and a sha256 digest name, and
// only the tags that pass the v1.16 tag rule.
func (v rawVersion) version() (Version, bool) {
	if v.ID <= 0 || !digestRe.MatchString(v.Name) {
		return Version{}, false
	}
	tags := []string{}
	for _, t := range v.Metadata.Container.Tags {
		if packageTagRe.MatchString(t) {
			tags = append(tags, t)
		}
	}
	return Version{ID: v.ID, Digest: v.Name, Tags: tags, CreatedAt: v.CreatedAt.UTC(), UpdatedAt: v.UpdatedAt.UTC()}, true
}

// apiCall sends one REST call with the user token and reads at most max
// bytes of the answer.
func (c *Client) apiCall(ctx context.Context, method, token, path string, max int) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.APIURL+path, nil)
	if err != nil {
		return nil, 0, ErrFailed
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", userAgent)
	body, _, status, err := c.doMax(req, max)
	if err != nil {
		return nil, 0, ErrFailed
	}
	return body, status, nil
}
