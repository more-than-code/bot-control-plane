package tools

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/more-than-code/bot-control-plane/mcp/internal/apiclient"
)

// Console deep links (the privileged-steps principle, api-v1 v1.51): an
// agent never does a privileged step itself. When the next step is the
// person's, a tool result carries console_url, the exact console page,
// already pointed at the change where the page supports it. Every link is
// built here, from ids the caller already checked as UUIDs and names that
// are query-escaped. With no BOT_MCP_CONSOLE_ORIGIN every link is "".

// linkRule is the one sentence tool descriptions use for console_url.
const linkRule = "When a result carries console_url, the next step is the person's: post that link in the chat for them to open; do not describe where to click."

func projectSettingsURL(api *apiclient.Client, projectID, anchor string) string {
	if !uuidPattern.MatchString(projectID) {
		return ""
	}
	path := "/projects/" + projectID + "?tab=settings"
	if anchor != "" {
		path += "#" + anchor
	}
	return api.ConsoleURL(path)
}

// envKeyURL opens a shared env set on project Settings with one key's row
// in view and its button focused: Reveal once (reveal) or Regenerate. The
// link only focuses the button; the person taps it (api-v1 v1.54).
func envKeyURL(api *apiclient.Client, projectID, set, key string, reveal bool) string {
	if !uuidPattern.MatchString(projectID) || !entryNamePattern.MatchString(set) || !envKeyPattern.MatchString(key) {
		return ""
	}
	do := "regenerate"
	if reveal {
		do = "reveal"
	}
	q := url.Values{}
	q.Set("tab", "settings")
	q.Set("env.set", set)
	q.Set("env.key", key)
	q.Set("env.do", do)
	return api.ConsoleURL("/projects/" + projectID + "?" + q.Encode() + "#env-sets")
}

func entryURL(api *apiclient.Client, projectID, entry string) string {
	if !uuidPattern.MatchString(projectID) || entry == "" {
		return ""
	}
	return api.ConsoleURL("/projects/" + projectID + "?tab=catalog&catalog.q=" + url.QueryEscape(entry))
}

func hostURL(api *apiclient.Client, hostID, anchor string) string {
	if !uuidPattern.MatchString(hostID) {
		return ""
	}
	path := "/hosts/" + hostID
	if anchor != "" {
		path += "#" + anchor
	}
	return api.ConsoleURL(path)
}

// hostPolicyURL opens the host's Policy tab with a change staged: the edge
// turned on and/or image patterns added. The person reviews it and taps
// Apply; the link never applies anything.
func hostPolicyURL(api *apiclient.Client, hostID string, edgeOn bool, images ...string) string {
	if !uuidPattern.MatchString(hostID) {
		return ""
	}
	q := []string{"tab=policy"}
	if edgeOn {
		q = append(q, "policy.edge=on")
	}
	for _, img := range images {
		if imagePatternRule.MatchString(img) {
			q = append(q, "policy.add_image="+url.QueryEscape(img))
		}
	}
	return api.ConsoleURL("/hosts/" + hostID + "?" + strings.Join(q, "&"))
}

func orgDomainsURL(api *apiclient.Client, orgID string) string {
	if !uuidPattern.MatchString(orgID) {
		return ""
	}
	return api.ConsoleURL("/orgs/" + orgID + "?tab=settings#domains")
}

// domainAssignURL opens the org's Domains with that domain's Assign dialog
// holding one more row: this project and pattern (api-v1 v1.52). The
// person reviews it and taps Save; the link never assigns anything.
func domainAssignURL(api *apiclient.Client, orgID, domainID, projectID, pattern string) string {
	if !uuidPattern.MatchString(orgID) || !uuidPattern.MatchString(domainID) || !uuidPattern.MatchString(projectID) ||
		len(pattern) > 253 || !assignLinkPattern.MatchString(pattern) {
		return ""
	}
	q := url.Values{}
	q.Set("tab", "settings")
	q.Set("domains.assign", domainID)
	q.Set("domains.project", projectID)
	q.Set("domains.pattern", pattern)
	return api.ConsoleURL("/orgs/" + orgID + "?" + q.Encode())
}

// fixURL is the console page for a readiness fix the person does, or "".
func fixURL(api *apiclient.Client, fix map[string]any, projectID string) string {
	str := func(k string) string { s, _ := fix[k].(string); return s }
	switch fix["action"] {
	case "connect_github", "add_registry_login":
		return projectSettingsURL(api, projectID, "registries")
	case "create_env_set":
		return projectSettingsURL(api, projectID, "env-sets")
	case "fill_value":
		if strings.HasPrefix(str("set"), "@") {
			return entryURL(api, projectID, str("set")[1:])
		}
		return projectSettingsURL(api, projectID, "env-sets")
	case "add_file":
		return projectSettingsURL(api, projectID, "files")
	case "grant_deploy_access":
		return hostURL(api, str("host_id"), "access")
	case "host_policy_snippet":
		return hostPolicyURL(api, str("host_id"), false, str("image_pattern"))
	case "allow_edge":
		return hostPolicyURL(api, str("host_id"), true)
	case "turn_on_edge":
		return hostURL(api, str("host_id"), "edge")
	case "verify_domain":
		return orgDomainsURL(api, str("org_id"))
	case "assign_domain":
		return domainAssignURL(api, str("org_id"), str("domain_id"), projectID, str("pattern"))
	default:
		return ""
	}
}

// withLink appends " Open: <url>" to text when url is set.
func withLink(text, link string) string {
	if link == "" {
		return text
	}
	return text + " Open: " + link
}

// GitHub refusal reasons (api-v1 v1.53): why ghcr.io refused the project's
// GitHub connection. The fix for org_restricted is on github.com, not in
// the console: an org owner grants the Deploybot GitHub app there.
var (
	githubLoginPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	githubGrantPattern = regexp.MustCompile(`^https://github\.com/settings/connections/applications/[A-Za-z0-9._-]{1,64}$`)
	refusalReasons     = map[string]bool{"github_sign_in": true, "org_restricted": true, "no_package_access": true}
)

// githubRefusal keeps an image-access refusal's allowlisted fields, or nil.
func githubRefusal(in map[string]any) map[string]any {
	reason, _ := in["reason"].(string)
	if !refusalReasons[reason] {
		return nil
	}
	out := map[string]any{"reason": reason}
	for key, re := range map[string]*regexp.Regexp{"owner": githubLoginPattern, "account": githubLoginPattern, "url": githubGrantPattern} {
		if s, ok := in[key].(string); ok && re.MatchString(s) {
			out[key] = s
		}
	}
	if reason == "org_restricted" && out["owner"] == nil {
		return nil
	}
	return out
}

// orgGrantStep is what the person does when an organization has not
// granted the Deploybot GitHub app.
func orgGrantStep(owner, link string) string {
	text := "send the person this GitHub link: an owner of " + owner + " clicks Grant next to " + owner + " (a member clicks Request, and an owner approves it), then check again; reconnecting GitHub will not help."
	if link == "" {
		return "ask an owner of " + owner + " to grant the Deploybot GitHub app in GitHub Settings → Applications → Authorized OAuth Apps (a member clicks Request); reconnecting GitHub will not help."
	}
	return text + " Open: " + link
}
