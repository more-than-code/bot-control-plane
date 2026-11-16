package audit

// Connect requests (api-v1 v1.31). The connect itself is
// registry.github_connected. Detail never holds the token or the request id,
// only the "request" reference (first 8 hex of the SHA-256 of the id).
const (
	ActionRegistryGitHubConnectRequested = "registry.github_connect_requested"
	ActionRegistryGitHubConnectDeclined  = "registry.github_connect_declined"
)
