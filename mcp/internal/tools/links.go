package tools

import (
	"net/url"
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
// link only focuses the button; the person taps it (api-v1 v1.52).
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
