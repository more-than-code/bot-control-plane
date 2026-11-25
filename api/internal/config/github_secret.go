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
	return checkGitHubSecret(line, name)
}

// LoadGitHubClientSecret is the Connect GitHub client secret from
// BOT_API_GITHUB_CLIENT_SECRET (value) or, when that is empty,
// BOT_API_GITHUB_CLIENT_SECRET_FILE (path; api-v1 v1.50 item 2), with the
// same rules.
func LoadGitHubClientSecret(value, path string) (string, error) {
	if value == "" {
		return ReadGitHubClientSecret(path)
	}
	const name = "BOT_API_GITHUB_CLIENT_SECRET"
	if err := envSecretLine(value, name); err != nil {
		return "", err
	}
	return checkGitHubSecret(value, name)
}

func checkGitHubSecret(line, name string) (string, error) {
	if strings.TrimSpace(line) == "" {
		return "", errors.New(name + " is empty")
	}
	if strings.ContainsAny(line, " \t\x00") {
		return "", errors.New(name + " is invalid")
	}
	return line, nil
}
