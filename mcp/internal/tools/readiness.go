package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/more-than-code/bot-control-plane/mcp/internal/apiclient"
)

// Readiness, image access and the host-policy snippet (api-v1 v1.29 items
// 6–8). Each result is rebuilt from allowlisted fields, so a value, a
// preview or a token never reaches the agent even if the API sent one.

// noSecretRule starts the three tool descriptions.
const noSecretRule = "No env value, secret preview or registry token is ever returned by this tool."

const checkReadinessBaseSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["project_id"],
  "properties": {
    "project_id": {"type": "string", "format": "uuid"},
    "entry": {"type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,47}$", "description": "A stored catalog entry. Pass exactly one of entry or spec."},
    "host_id": {"type": "string", "format": "uuid", "description": "Optional. With it, the host checks run too: deploy access, online, containers, image policy and shared networks."}
  }
}`

// checkReadinessSchema is the base schema plus spec, the same ImageSpec
// schema create_catalog_entry takes (api-v1 v1.32 item 3).
var checkReadinessSchema = readinessSchemaWithSpec()

func readinessSchemaWithSpec() string {
	var write, base map[string]any
	if err := json.Unmarshal([]byte(catalogWriteSchema), &write); err != nil {
		panic(err)
	}
	if err := json.Unmarshal([]byte(checkReadinessBaseSchema), &base); err != nil {
		panic(err)
	}
	spec, _ := write["properties"].(map[string]any)["spec"].(map[string]any)
	if spec == nil {
		panic("catalogWriteSchema has no spec")
	}
	spec["description"] = "An image entry spec, as create_catalog_entry takes, checked before the entry exists. Pass exactly one of entry or spec."
	base["properties"].(map[string]any)["spec"] = spec
	out, err := json.Marshal(base)
	if err != nil {
		panic(err)
	}
	return string(out)
}

const imageAccessSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["project_id", "image"],
  "properties": {
    "project_id": {"type": "string", "format": "uuid"},
    "image": {"type": "string", "maxLength": 512, "description": "Image reference, for example ghcr.io/acme/api:v1 or nginx."}
  }
}`

const hostPolicySnippetSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["project_id", "entry", "host_id"],
  "properties": {
    "project_id": {"type": "string", "format": "uuid"},
    "entry": {"type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,47}$"},
    "host_id": {"type": "string", "format": "uuid"}
  }
}`

var (
	readyCheckNames = map[string]bool{
		"env_sets": true, "env_values": true, "env_refs": true, "env_pairs": true, "files": true, "registry": true,
		"host_access": true, "host_online": true, "host_images": true, "host_policy": true, "networks": true, "routes": true,
	}
	readyStatuses = map[string]bool{"ok": true, "missing": true, "warn": true, "unknown": true}
	imageAccesses = map[string]bool{
		"public": true, "private_ok": true, "private_no_login": true, "login_failed": true, "not_found": true, "unknown": true,
	}
	imageLogins = map[string]bool{"none": true, "manual": true, "github": true}
	// registryHostPattern is a registry host with an optional port.
	registryHostPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,252})(:[0-9]{1,5})?$`)
	// fileNamePattern is a managed file name (api-v1 v1.9).
	fileNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	// imagePatternRule is an image repository: registry host and path.
	imagePatternRule = regexp.MustCompile(`^[a-z0-9.-]+(:[0-9]{1,5})?(/[a-z0-9._-]+)+$`)
	// projectSlugPattern is the project slug rule (store.Slug: up to 80 characters).
	projectSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)
)

const settingsHint = "project Settings"

func registerReadiness(server *mcp.Server, api *apiclient.Client) {
	addTool(server, &mcp.Tool{
		Name: "check_readiness",
		Description: noSecretRule + " Check whether a catalog entry is ready to deploy, before deploy_entry, or whether an image spec would be, before create_catalog_entry: pass exactly one of entry (a stored entry) or spec (the ImageSpec create_catalog_entry takes; nothing is stored, and env sets, keys and files it names may still be missing). Checks: env sets exist, env keys have values, ${KEY} references resolve, managed files exist, " +
			"the image registry is reachable with the stored login, and, with host_id, deploy access, the host being online, containers, the host's image policy and shared networks, and for an entry with routes (set_routes), the routes check: a verified domain, a published port and the edge on the entry's host (an org admin turns the edge on in the console). " +
			"Each check has a status (ok, missing, warn, unknown) and a fix. Fixes you can do yourself: fill_value with request_env_value, create_env_set with create_env_set, add_file with request_file (the person adds the file in the browser); " +
			"you can start connect_github with connect_registry, and the person approves on github.com. " +
			"Fixes for the person (add_registry_login, grant_deploy_access, host_policy_snippet, allow_edge, turn_on_edge, verify_domain, assign_domain): the fix carries console_url, the console page for that step, already pointed at the change; post it for the person. " +
			"Never edit a host's runner.toml or enter a secret yourself. For host_policy_snippet on a host whose root has not let the console manage its policy, also call host_policy_snippet and show the person the snippet. " + linkRule + " Advisory only: deploy keeps its own refusals. " +
			"Needs owner or admin, or the entry's deploy permission on a host the project deploys to. Reads stored API data and may ask the image registry (cached). Does not contact any host.",
		Annotations: readOnlyAnnotations(false),
		InputSchema: json.RawMessage(checkReadinessSchema),
	}, func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct {
			ProjectID string          `json:"project_id"`
			Entry     string          `json:"entry"`
			Spec      json.RawMessage `json:"spec"`
			HostID    string          `json:"host_id"`
		}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		if err := requireUUID("project_id", in.ProjectID); err != nil {
			return badRequest(err)
		}
		if (in.Entry == "") == (len(in.Spec) == 0) {
			return badRequest(errors.New("pass exactly one of entry or spec"))
		}
		if in.HostID != "" {
			if err := requireUUID("host_id", in.HostID); err != nil {
				return badRequest(err)
			}
		}
		var payload json.RawMessage
		var fail *mcp.CallToolResult
		label := in.Entry
		if in.Entry != "" {
			if err := requireEntryName(in.Entry); err != nil {
				return badRequest(err)
			}
			q := url.Values{}
			if in.HostID != "" {
				q.Set("host_id", in.HostID)
			}
			payload, fail = get(ctx, api, idPath("v1", "projects", in.ProjectID, "catalog", in.Entry, "readiness"), q)
		} else {
			if err := validateImageSpec(in.Spec); err != nil {
				return badRequest(err)
			}
			body := struct {
				Spec   json.RawMessage `json:"spec"`
				HostID string          `json:"host_id,omitempty"`
			}{in.Spec, in.HostID}
			payload, fail = post(ctx, api, idPath("v1", "projects", in.ProjectID, "catalog", "readiness"), body)
			label = "This spec"
		}
		if fail != nil {
			return fail
		}
		out, summary, err := projectReadinessLinks(api, payload, in.ProjectID, label)
		if err != nil {
			return toolError("error", err.Error())
		}
		return present(out, summary)
	})

	addTool(server, &mcp.Tool{
		Name: "image_access",
		Description: noSecretRule + " Say whether an image can be pulled with the project's registry logins: public (no login needed), private_ok (the stored login works), " +
			"private_no_login (private or missing) or login_failed (the stored login was refused): for ghcr.io call connect_registry, which gives the person a link to approve on github.com; for another registry only the person can add or update the login, and console_url is the page to send them. not_found, or unknown. " +
			"For a refused GitHub connection, refusal says why when GitHub told: github_sign_in (reconnect GitHub), org_restricted (the owner organization has not granted the Deploybot GitHub app; github_url is the GitHub page where an org owner clicks Grant, so send the person that link instead of reconnecting), or no_package_access (the connected account cannot see the package). " +
			"login says which login is stored: none, manual or github. Needs owner or admin. Asks the image registry (cached 60 s, 20 lookups per minute). Does not contact any host.",
		Annotations: readOnlyAnnotations(false),
		InputSchema: json.RawMessage(imageAccessSchema),
	}, func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct {
			ProjectID string `json:"project_id"`
			Image     string `json:"image"`
		}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		if err := requireUUID("project_id", in.ProjectID); err != nil {
			return badRequest(err)
		}
		if in.Image == "" || len(in.Image) > 512 || strings.ContainsAny(in.Image, " \t\r\n") {
			return badRequest(errors.New("image must be an image reference"))
		}
		q := url.Values{}
		q.Set("image", in.Image)
		payload, fail := get(ctx, api, idPath("v1", "projects", in.ProjectID, "image-access"), q)
		if fail != nil {
			return fail
		}
		var body struct {
			Registry string         `json:"registry"`
			Access   string         `json:"access"`
			Login    string         `json:"login"`
			Refusal  map[string]any `json:"refusal"`
		}
		if err := json.Unmarshal(payload, &body); err != nil || !imageAccesses[body.Access] || !imageLogins[body.Login] || !registryHostPattern.MatchString(body.Registry) {
			return toolError("error", errStatusShape.Error())
		}
		result := map[string]any{"registry": body.Registry, "access": body.Access, "login": body.Login}
		summary := accessSummary(body.Registry, body.Access, body.Login)
		var refusal map[string]any
		if body.Access == "login_failed" && body.Login == "github" {
			refusal = githubRefusal(body.Refusal)
		}
		if refusal != nil {
			result["refusal"] = refusal
			summary = refusalSummary(refusal)
		}
		if refusal != nil && refusal["reason"] == "org_restricted" {
			// The fix is on github.com; the console cannot do it.
			if link, _ := refusal["url"].(string); link != "" {
				result["github_url"] = link
			}
		} else if (body.Access == "private_no_login" || body.Access == "login_failed") && body.Registry != connectRegistryHost {
			if link := projectSettingsURL(api, in.ProjectID, "registries"); link != "" {
				result["console_url"] = link
				summary = withLink(summary, link) + " Send the person that link; do not describe where to click."
			}
		}
		out, err := json.Marshal(result)
		if err != nil {
			return toolError("error", "internal error")
		}
		return present(out, summary)
	})

	addTool(server, &mcp.Tool{
		Name: "host_policy_snippet",
		Description: noSecretRule + " Get the lines a host's root adds to /etc/bot-agent/runner.toml so the host allows this image entry: the image pattern (the entry's repository), " +
			"the project slug, the runner.toml lines and the matching install.sh command. Show the snippet to the person: only the host's root can apply it, " +
			"over SSH or Cloud Assistant, unless the root set console_policy = true: then an org owner or admin allows the image on the host's console Policy tab (console_url opens it with the change staged). The agent never edits runner.toml. " + linkRule + " Use it when check_readiness reports host_policy missing. Needs owner or admin. Text only. Does not contact any host.",
		Annotations: readOnlyAnnotations(false),
		InputSchema: json.RawMessage(hostPolicySnippetSchema),
	}, func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct {
			ProjectID string `json:"project_id"`
			Entry     string `json:"entry"`
			HostID    string `json:"host_id"`
		}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		if err := requireUUID("project_id", in.ProjectID); err != nil {
			return badRequest(err)
		}
		if err := requireEntryName(in.Entry); err != nil {
			return badRequest(err)
		}
		if err := requireUUID("host_id", in.HostID); err != nil {
			return badRequest(err)
		}
		q := url.Values{}
		q.Set("host_id", in.HostID)
		payload, fail := get(ctx, api, idPath("v1", "projects", in.ProjectID, "catalog", in.Entry, "host-policy-snippet"), q)
		if fail != nil {
			return fail
		}
		var body struct {
			ImagePattern string `json:"image_pattern"`
			Project      string `json:"project"`
			Note         string `json:"note"`
		}
		if err := json.Unmarshal(payload, &body); err != nil || !imagePatternRule.MatchString(body.ImagePattern) || !projectSlugPattern.MatchString(body.Project) {
			return toolError("error", errStatusShape.Error())
		}
		// The text is rebuilt from the two checked names.
		toml := fmt.Sprintf("images = [%q]\nprojects = [%q]\n", body.ImagePattern, body.Project)
		command := fmt.Sprintf("sudo ./install.sh install --runner --project %s --image '%s'", body.Project, body.ImagePattern)
		note := clip(publicString(body.Note), maxErrorBytes)
		link := hostPolicyURL(api, in.HostID, false, body.ImagePattern)
		result := map[string]any{
			"image_pattern": body.ImagePattern, "project": body.Project, "toml": toml, "command": command, "note": note,
		}
		if link != "" {
			result["console_url"] = link
		}
		out, err := json.Marshal(result)
		if err != nil {
			return toolError("error", "internal error")
		}
		summary := "Show this to the person; the host's root applies it (Deploybot never edits runner.toml).\n" +
			"Add to /etc/bot-agent/runner.toml, keeping the entries already there:\n" + toml + "Or, for a new policy: " + command + "\n" + note
		if link != "" {
			summary = "Send the person this link: on a host whose root set console_policy = true, an org owner or admin allows the image on the host's Policy tab, with the change already staged: " + link + "\n" +
				"On a host without it, the host's root applies the snippet instead.\n" + summary
		}
		return present(out, summary)
	})
}

// readyRow is one allowlisted readiness check.
type readyRow struct {
	Check   string         `json:"check"`
	Status  string         `json:"status"`
	Message string         `json:"message"`
	Fix     map[string]any `json:"fix"`
	Next    string         `json:"next,omitempty"`
}

// projectReadiness is projectReadinessLinks with no console links.
func projectReadiness(payload json.RawMessage, projectID, entry string) (json.RawMessage, string, error) {
	return projectReadinessLinks(nil, payload, projectID, entry)
}

// projectReadinessLinks rebuilds the readiness answer from allowlisted
// fields and adds, per check, what to do next and, for a step the person
// does, console_url.
func projectReadinessLinks(api *apiclient.Client, payload json.RawMessage, projectID, entry string) (json.RawMessage, string, error) {
	var body struct {
		Ready  bool `json:"ready"`
		Checks []struct {
			Check   string         `json:"check"`
			Status  string         `json:"status"`
			Message string         `json:"message"`
			Fix     map[string]any `json:"fix"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(payload, &body); err != nil || len(body.Checks) == 0 || len(body.Checks) > 200 {
		return nil, "", errStatusShape
	}
	rows := make([]readyRow, 0, len(body.Checks))
	var b strings.Builder
	missing := 0
	for _, c := range body.Checks {
		if !readyCheckNames[c.Check] || !readyStatuses[c.Status] {
			return nil, "", errStatusShape
		}
		fix := projectFix(c.Fix)
		row := readyRow{Check: c.Check, Status: c.Status, Message: readinessMessage(c.Message), Fix: fix}
		if c.Status != "ok" {
			// A step the agent does with a tool gets no console link: the
			// tool's own result carries the person's link.
			link := ""
			if personStep(fix) {
				link = fixURL(api, fix, projectID)
			}
			row.Next = nextStep(fix, projectID, entry, link)
			if link != "" {
				fix["console_url"] = link
			}
		}
		if c.Status == "missing" {
			missing++
		}
		rows = append(rows, row)
		fmt.Fprintf(&b, "\n- %s: %s", row.Check, row.Status)
		if row.Status != "ok" {
			fmt.Fprintf(&b, " (%s)", row.Message)
			if row.Next != "" {
				fmt.Fprintf(&b, " Next: %s", row.Next)
			}
		}
	}
	out, err := json.Marshal(map[string]any{"ready": body.Ready, "checks": rows})
	if err != nil {
		return nil, "", err
	}
	head := fmt.Sprintf("%s is ready to deploy as far as Deploybot can check.", entry)
	if !body.Ready {
		head = fmt.Sprintf("%s is not ready: %d check(s) missing.", entry, missing)
	}
	return out, head + " No value or token is shown." + b.String(), nil
}

// missingKeyMsg is the readiness text for a secret_env source key that does
// not exist. Like the v1.27 env refusals it holds names only.
var missingKeyMsg = regexp.MustCompile(`^env [A-Z_][A-Z0-9_]{0,63} in set @?[a-z0-9][a-z0-9-]{0,47} does not exist$`)

// pairMsg is the env_pairs warning (api-v1 v1.33 item 5): key and set names
// and fixed text only, so it passes unredacted like the env refusals.
var pairMsg = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63} comes from (the entry's env|env set [a-z0-9][a-z0-9-]{0,47}|the entry's private set( @[a-z0-9][a-z0-9-]{0,47})?) but [A-Z_][A-Z0-9_]{0,63} comes from (the entry's env|env set [a-z0-9][a-z0-9-]{0,47}|the entry's private set( @[a-z0-9][a-z0-9-]{0,47})?); keep a pair in one place$`)

// draftPrivateMsg is the readiness-from-spec row of a secret_env key given
// as {"fill": true} (api-v1 v1.33 item 6): a key name and fixed text.
var draftPrivateMsg = regexp.MustCompile(`^env [A-Z_][A-Z0-9_]{0,63} in the entry's private set has no value yet; after the entry is created, fill it with request_env_value$`)

// readinessMessage passes the fixed env texts unredacted (the keyword rule
// would hide the key the person has to fix) and redacts anything else.
func readinessMessage(msg string) string {
	if envRefusal(msg) || missingKeyMsg.MatchString(msg) || pairMsg.MatchString(msg) || draftPrivateMsg.MatchString(msg) {
		return msg
	}
	return clip(publicString(msg), maxErrorBytes)
}

// projectFix keeps the action and only the names that fit their rules.
func projectFix(in map[string]any) map[string]any {
	action, _ := in["action"].(string)
	switch action {
	case "create_env_set", "fill_value", "add_file", "connect_github", "add_registry_login", "grant_deploy_access", "host_policy_snippet",
		"allow_edge", "turn_on_edge", "verify_domain", "assign_domain", "github_org_access", "none":
	default:
		return map[string]any{"action": "none"}
	}
	out := map[string]any{"action": action}
	keep := func(key string, re *regexp.Regexp) {
		if s, ok := in[key].(string); ok && re.MatchString(s) {
			out[key] = s
		}
	}
	keep("set", anySetPattern)
	keep("key", envKeyPattern)
	keep("name", fileNamePattern)
	keep("registry", registryHostPattern)
	keep("host_id", uuidPattern)
	keep("org_id", uuidPattern)
	keep("image_pattern", imagePatternRule)
	if action == "github_org_access" {
		// api-v1 v1.53: the organization and GitHub's grant page.
		keep("owner", githubLoginPattern)
		keep("url", githubGrantPattern)
		if out["owner"] == nil {
			return map[string]any{"action": "none"}
		}
	}
	if action == "assign_domain" {
		// api-v1 v1.52: only the org, the domain, the pattern and its
		// written-out name mean anything on this fix; fixURL builds the
		// Assign link from them. The agent never assigns anything itself.
		out = map[string]any{"action": action}
		keep("org_id", uuidPattern)
		keep("domain_id", uuidPattern)
		keep("name", routeHostPattern)
		if p, ok := in["pattern"].(string); ok && len(p) <= 253 && assignLinkPattern.MatchString(p) {
			out["pattern"] = p
		}
	}
	return out
}

// personStep reports whether a fix is the person's to do in the console,
// as opposed to a tool the agent calls (which brings its own link) or a
// step on github.com.
func personStep(fix map[string]any) bool {
	str := func(k string) string { s, _ := fix[k].(string); return s }
	switch fix["action"] {
	case "fill_value":
		return str("key") == ""
	case "add_file":
		return !managedFilePattern.MatchString(str("name"))
	case "create_env_set", "connect_github", "github_org_access":
		return false
	}
	return true
}

// askPerson is a person's console step. With link it hands the person the
// exact page; without (no BOT_MCP_CONSOLE_ORIGIN) it names the place.
func askPerson(who, action, where, link string) string {
	if link != "" {
		return fmt.Sprintf("send %s this link, which opens the page for it, to %s: %s", who, action, link)
	}
	return fmt.Sprintf("ask %s to %s in the console (%s).", who, action, where)
}

// nextStep is the agent's next move for one fix: a tool it can call, or
// the person's console step, as a link when link is set.
func nextStep(fix map[string]any, projectID, entry, link string) string {
	str := func(k string) string { s, _ := fix[k].(string); return s }
	const person, admin = "the person", "an org owner or admin"
	switch fix["action"] {
	case "fill_value":
		if str("set") == "" && str("key") != "" {
			// A spec's private key (api-v1 v1.33 item 6): no entry exists yet.
			return fmt.Sprintf("after create_catalog_entry and confirm, call request_env_value with project_id %s, entry (the new entry's name) and key %s; the person enters the value.", projectID, str("key"))
		}
		if set := str("set"); strings.HasPrefix(set, "@") && str("key") != "" {
			return fmt.Sprintf("call request_env_value with project_id %s, entry %s and key %s; the person enters the value.", projectID, set[1:], str("key"))
		}
		if str("key") != "" {
			return fmt.Sprintf("call request_env_value with project_id %s, set %s and key %s; the person enters the value.", projectID, str("set"), str("key"))
		}
		return askPerson(person, "fix the reference in env set "+str("set"), settingsHint+", Env sets", link)
	case "create_env_set":
		return fmt.Sprintf("call create_env_set to create env set %s with the key names the entry needs.", str("set"))
	case "connect_github":
		return fmt.Sprintf("call connect_registry with project_id %s and registry ghcr.io; the person opens the link and approves on github.com.", projectID)
	case "add_registry_login":
		return askPerson(person, "add a registry login for "+str("registry"), settingsHint+", Registries", link)
	case "add_file":
		if managedFilePattern.MatchString(str("name")) {
			return fmt.Sprintf("call request_file with project_id %s and name %s; the person adds the file in the browser.", projectID, str("name"))
		}
		return askPerson(person, "add file "+str("name"), settingsHint+", Files", link)
	case "grant_deploy_access":
		return askPerson(admin, "grant this project deploy access to the host", "the host's Access section", link)
	case "assign_domain":
		name := "that name"
		if str("name") != "" {
			name = str("name")
		}
		return askPerson(admin, "assign "+name+" to this project (assigning a domain is console only)", "the organization's Domains, Assign", link)
	case "host_policy_snippet":
		return askPerson(admin, "allow the image on the host's Policy tab", "the host's Policy tab", link) +
			fmt.Sprintf(" If the host's root has not set console_policy = true, call host_policy_snippet with project_id %s, entry %s and host_id %s, and show the person the snippet.", projectID, entry, str("host_id"))
	case "allow_edge":
		return askPerson(admin, "allow the edge", "the host's Policy tab", link) +
			" The host's root must have set console_policy = true; otherwise the root adds edge = true to runner.toml."
	case "turn_on_edge":
		return askPerson(admin, "turn the edge on", "the host's Edge section", link)
	case "verify_domain":
		return askPerson(admin, "add, verify and assign the domain to this project", "the organization's Domains", link)
	case "github_org_access":
		return orgGrantStep(str("owner"), str("url"))
	default:
		return ""
	}
}

// refusalSummary is the image_access summary when GitHub told why ghcr.io
// refused the GitHub connection (api-v1 v1.53).
func refusalSummary(r map[string]any) string {
	str := func(k string) string { s, _ := r[k].(string); return s }
	switch str("reason") {
	case "org_restricted":
		return "ghcr.io refused the GitHub connection because the " + str("owner") + " organization has not granted the Deploybot GitHub app access to its packages. Next: " + orgGrantStep(str("owner"), str("url"))
	case "no_package_access":
		account := "The connected GitHub account"
		if str("account") != "" {
			account = "The GitHub account " + str("account")
		}
		return "ghcr.io refused the GitHub connection. " + account + " cannot see the " + str("owner") + " package: ask the person to give it read access to the package on GitHub, or to reconnect GitHub as an account that can (call connect_registry; the person opens its link)."
	default:
		return "The GitHub sign-in for ghcr.io expired, was revoked or lost read:packages. Call connect_registry with this project and registry ghcr.io to reconnect GitHub; the person opens its link and approves on github.com."
	}
}

func accessSummary(registry, access, login string) string {
	switch access {
	case "public":
		return "Public image, no login needed."
	case "private_ok":
		if login == "github" {
			return "Private image; the GitHub connection can pull it."
		}
		return "Private image; the stored login for " + registry + " can pull it."
	case "private_no_login":
		if registry == "ghcr.io" {
			return "Private or missing, and no login is stored. Call connect_registry with this project and registry ghcr.io; the person opens its link and approves on github.com."
		}
		return "Private or missing, and no login is stored. Only the person can add a registry login for " + registry + ", in the console (" + settingsHint + ", Registries)."
	case "login_failed":
		if registry == "ghcr.io" {
			return "The stored login for ghcr.io was refused. Call connect_registry with this project and registry ghcr.io to reconnect GitHub; the person opens its link and approves on github.com."
		}
		return "The stored login for " + registry + " was refused. Only the person can update it, in the console (" + settingsHint + ", Registries)."
	case "not_found":
		return "The registry says this image does not exist."
	default:
		return "The registry could not be checked (timeout, unreachable, or a registry Deploybot does not look up)."
	}
}

const sharedNetworkSnippetSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["project_id", "host_id", "network"],
  "properties": {
    "project_id": {"type": "string", "format": "uuid"},
    "host_id": {"type": "string", "format": "uuid"},
    "network": {"type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,31}$", "description": "Shared network name."},
    "subnet": {"type": "string", "description": "IPv4 CIDR inside 10.0.0.0/8, 172.16.0.0/12 or 192.168.0.0/16, prefix 16 to 28, for example 10.200.10.0/24. Leave out when the host already defines the network."}
  }
}`

// snippetLine is one runner.toml line the network snippet may hold.
var snippetLine = regexp.MustCompile(`^(# [ -~]{1,200}|shared_networks = \["[a-z0-9][a-z0-9-]{0,31}=[0-9./]{9,18}"\]|shared_network_projects = \["[a-z0-9][a-z0-9-]{0,31}:[a-z0-9][a-z0-9-]{0,79}"\])$`)

// registerSharedNetworkSnippet is api-v1 v1.33 item 7.
func registerSharedNetworkSnippet(server *mcp.Server, api *apiclient.Client) {
	addTool(server, &mcp.Tool{
		Name: "shared_network_snippet",
		Description: noSecretRule + " Get the runner.toml lines a host's root adds so a shared network on that host is open to this project (shared_networks and shared_network_projects). " +
			"Show the snippet to the person: only the host's root can apply it, over SSH or Cloud Assistant; Deploybot never edits runner.toml or creates a network. " +
			"Use it when list_hosts shows no shared network open to the project, or a deploy was refused for a closed network. Needs owner or admin. Text only. Does not contact any host.",
		Annotations: readOnlyAnnotations(false),
		InputSchema: json.RawMessage(sharedNetworkSnippetSchema),
	}, func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct {
			ProjectID string `json:"project_id"`
			HostID    string `json:"host_id"`
			Network   string `json:"network"`
			Subnet    string `json:"subnet"`
		}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		if err := requireUUID("project_id", in.ProjectID); err != nil {
			return badRequest(err)
		}
		if err := requireUUID("host_id", in.HostID); err != nil {
			return badRequest(err)
		}
		if !networkNamePattern.MatchString(in.Network) {
			return badRequest(errors.New("network must be 1 to 32 lowercase letters, digits or dashes"))
		}
		q := url.Values{}
		q.Set("network", in.Network)
		if in.Subnet != "" {
			q.Set("subnet", in.Subnet)
		}
		payload, fail := get(ctx, api, idPath("v1", "projects", in.ProjectID, "hosts", in.HostID, "network-snippet"), q)
		if fail != nil {
			return fail
		}
		var body struct {
			Snippet string `json:"snippet"`
		}
		if err := json.Unmarshal(payload, &body); err != nil || body.Snippet == "" || len(body.Snippet) > 1024 {
			return toolError("error", errStatusShape.Error())
		}
		for _, line := range strings.Split(strings.TrimSuffix(body.Snippet, "\n"), "\n") {
			if !snippetLine.MatchString(line) {
				return toolError("error", errStatusShape.Error())
			}
		}
		out, err := json.Marshal(map[string]any{"snippet": body.Snippet})
		if err != nil {
			return toolError("error", "internal error")
		}
		return present(out, "Show this to the person; the host's root applies it (Deploybot never edits runner.toml):\n"+body.Snippet)
	})
}
