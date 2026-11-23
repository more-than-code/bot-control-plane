# Say why ghcr.io refused the GitHub connection (api-v1 v1.53)

Status: rev 1, as implemented (2026-11-23). Covers DD item
DEF-20261123-registry-refused-gives-no-reason-when-gi-ogc2, under the
privileged-steps principle (DEF-20261123-design-principle-privileged-steps-are-do-exut):
the agent never does the fix itself; it passes on the one link that does.

## 1. Problem

After Connect GitHub, ghcr.io answered `denied` for an image owned by a GitHub
organization with OAuth App access restrictions. Deploybot reported
`login_failed` and every surface said "reconnect GitHub". Reconnecting cannot
help: the organization has to grant the Deploybot OAuth app first.

## 2. Diagnosis (API)

When an image-access lookup ends `login_failed` with the GitHub connection
(`login` `github`, registry `ghcr.io`), the API asks the GitHub REST API with
the same token that ghcr.io refused (no second token read or refresh):

1. `GET /user`. A 401 is `github_sign_in`. A token without `read:packages` in
   `X-OAuth-Scopes` is `github_sign_in` too.
2. When the image's owner (first path segment) is the connected login
   (case-insensitive), GitHub gives no further reason: no refusal.
3. Otherwise `GET /orgs/{owner}/packages/container/{name}`:
   - 403 whose body names "OAuth App access restrictions": `org_restricted`.
   - 404: `no_package_access` (a user owner, or an org package this account
     cannot see).
   - anything else: no refusal.

No GitHub body is returned, logged or stored; only the 403's marker phrase is
read. A failed call means no refusal. The answer is cached with the access
answer (60 s), so a cached read asks GitHub nothing. `read:packages` already
covers these calls: no new scope.

## 3. Contract additions

- `ImageAccess.refusal` (optional; only with `access` `login_failed` and
  `login` `github`, and only when GitHub told):
  `{ "reason": "github_sign_in" | "org_restricted" | "no_package_access",
  "owner"?: string, "account"?: string, "url"?: string }`. `owner` is the
  image's owner, `account` the connected GitHub login, `url` (org_restricted
  only) `https://github.com/settings/connections/applications/<client_id>`,
  where an org owner clicks Grant and a member clicks Request.
- Readiness fix action `github_org_access` with `registry`, `owner` and `url`,
  for `org_restricted`. The `registry` check message names the organization
  and says reconnecting will not help. `no_package_access` keeps
  `connect_github` with a message naming the account and the package owner;
  `github_sign_in` says the sign-in expired, was revoked or lost
  `read:packages`.
- The `registry_login` problem detail (bell, `list_problems`) says the same,
  with the GitHub URL for `org_restricted` (within the 300-character limit).
  `github_sign_in` and an unknown reason keep the v1.43 detail.

No migration. Manual logins are never diagnosed.

## 4. Consumers

- MCP `image_access`: `refusal` (allowlisted fields; `url` must be GitHub's
  application page) and, for `org_restricted`, `github_url` instead of a
  console link; the summary tells the agent to send that link and not to ask
  for a reconnect.
- MCP `check_readiness`: the `github_org_access` fix keeps `owner` and `url`;
  `next` is "send the person this GitHub link …". No `console_url`.
- MCP `list_problems`: a `registry_login` line carries its detail; with the
  GitHub link it says to send that link instead of `connect_registry`.
- Console: the image line says which organization has not granted the app,
  with Grant on GitHub; the readiness row's button is Grant on GitHub; the
  Registries card's Refused badge tooltip and a line per reason (organizations
  first, at most 5) say why, with Grant on GitHub.
