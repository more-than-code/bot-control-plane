package config

import (
	"errors"
	"strings"
)

// ReadGitHubClientSecret reads BOT_API_GITHUB_CLIENT_SECRET_FILE like the
// secrets key (readSecretFile): a regular non-symlink file with mode 0600
// whose first line is the secret. The secret and the path are never
// included in an error.
func ReadGitHubClientSecret(path string) (string, error) {
	const name = "BOT_API_GITHUB_CLIENT_SECRET_FILE"
	if strings.TrimSpace(path) == "" {
		return "", errors.New(name + " is required")
	}
	line, err := readSecretFile(path, name)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(line) == "" {
		return "", errors.New(name + " is empty")
	}
	if strings.ContainsAny(line, " \t\x00") {
		return "", errors.New(name + " is invalid")
	}
	return line, nil
}
