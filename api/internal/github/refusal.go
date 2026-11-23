package github

// Why ghcr.io refused a Connect GitHub login (api-v1 v1.52): the registry
// answers only "denied", so the API asks the GitHub REST API with the same
// token. No GitHub body is returned, logged or echoed; the only thing read
// from one is whether a 403 names OAuth App access restrictions.

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// Refusal reasons. An empty reason is "could not tell".
const (
	// RefusalSignIn: GitHub refused the token itself (401), or the token
	// lost read:packages. Reconnecting GitHub fixes it.
	RefusalSignIn = "github_sign_in"
	// RefusalOrgRestricted: the image's owner is an organization with OAuth
	// App access restrictions that has not granted this app.
	RefusalOrgRestricted = "org_restricted"
	// RefusalNoPackageAccess: the token works but the connected account
	// cannot see the package.
	RefusalNoPackageAccess = "no_package_access"
)

// restrictedMarker is the phrase GitHub's 403 uses for an organization's
// OAuth App access restrictions.
const restrictedMarker = "oauth app access restrictions"

// Refusal is the diagnosis of one refused ghcr.io pull.
type Refusal struct {
	Reason string
	// Owner is the image's owner (the first path segment) for
	// org_restricted and no_package_access.
	Owner string
	// Account is the connected GitHub login, when GitHub named it.
	Account string
}

// ApplicationURL is the page where the connected account sees this OAuth
// App and its organization access: an org owner clicks Grant there, a
// member clicks Request.
func (c *Client) ApplicationURL() string {
	if !clientIDOK(c.cfg.ClientID) {
		return ""
	}
	return "https://github.com/settings/connections/applications/" + url.PathEscape(c.cfg.ClientID)
}

// DiagnoseRefusal tells why ghcr.io refused token for the repository path
// (owner/name). It reads GET /user, then, when the image's owner is not
// the connected account, the owner's container package. An empty Reason
// means it could not tell; err is only a failed call.
func (c *Client) DiagnoseRefusal(ctx context.Context, token, path string) (Refusal, error) {
	login, scopes, status, err := c.user(ctx, token)
	if err != nil {
		return Refusal{}, err
	}
	if status == http.StatusUnauthorized {
		return Refusal{Reason: RefusalSignIn}, nil
	}
	if status != http.StatusOK {
		return Refusal{}, nil
	}
	if !HasScope(scopes) {
		return Refusal{Reason: RefusalSignIn, Account: login}, nil
	}
	owner, name, ok := SplitGHCRPath(path)
	if !ok || strings.EqualFold(owner, login) {
		// The account's own package: GitHub gives no further reason.
		return Refusal{Account: login}, nil
	}
	body, status, err := c.apiCall(ctx, http.MethodGet, token,
		"/orgs/"+url.PathEscape(owner)+"/packages/container/"+url.PathEscape(name), bodyMax)
	if err != nil {
		return Refusal{}, err
	}
	switch {
	case status == http.StatusForbidden && strings.Contains(strings.ToLower(string(body)), restrictedMarker):
		return Refusal{Reason: RefusalOrgRestricted, Owner: owner, Account: login}, nil
	case status == http.StatusNotFound:
		// Not an org, or an org package this account cannot see.
		return Refusal{Reason: RefusalNoPackageAccess, Owner: owner, Account: login}, nil
	default:
		return Refusal{Account: login}, nil
	}
}

// user is User that also answers the status, so a 401 is told apart from
// a failed call.
func (c *Client) user(ctx context.Context, token string) (string, []string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.APIURL+"/user", nil)
	if err != nil {
		return "", nil, 0, ErrFailed
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", userAgent)
	body, header, status, err := c.do(req)
	if err != nil {
		return "", nil, 0, err
	}
	if status != http.StatusOK {
		return "", nil, status, nil
	}
	login, scopes, ok := parseUser(body, header)
	if !ok {
		return "", nil, 0, ErrFailed
	}
	return login, scopes, status, nil
}

// clientIDOK is the shape of a GitHub OAuth App client id.
func clientIDOK(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
