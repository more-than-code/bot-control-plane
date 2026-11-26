package config

import "errors"

// agentReleaseWebhookName is the env name of the bot-agent release webhook
// secret (api-v1 v1.62).
const agentReleaseWebhookName = "BOT_API_AGENT_RELEASE_WEBHOOK_SECRET"

// LoadAgentReleaseWebhookSecret is BOT_API_AGENT_RELEASE_WEBHOOK_SECRET
// (value) or, when that is empty, the first line of
// BOT_API_AGENT_RELEASE_WEBHOOK_SECRET_FILE (path; the v1.50 file rules).
// Both empty is "" (no webhook). A set secret is one line of at least 32
// characters. Errors name only the variable.
func LoadAgentReleaseWebhookSecret(value, path string) (string, error) {
	name := agentReleaseWebhookName
	line := value
	if line == "" {
		if path == "" {
			return "", nil
		}
		name += "_FILE"
		var err error
		if line, err = readSecretFile(path, name); err != nil {
			return "", err
		}
	} else if err := envSecretLine(value, name); err != nil {
		return "", err
	}
	if len(line) < 32 {
		return "", errors.New(agentReleaseWebhookName + " must be at least 32 characters")
	}
	return line, nil
}
