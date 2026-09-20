package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

// credentialOptions binds CLI flags for supplying a repository passphrase and salt, with
// decreasing-safety fallbacks: an env var, a file, or (least safe) a plaintext flag value visible
// in shell history and via /proc/<pid>/cmdline to any local user.
type credentialOptions struct {
	passphrase     *string
	passphraseFile *string
	salt           *string
	saltFile       *string
}

func bindCredentialOptions(flags *flag.FlagSet) *credentialOptions {
	return &credentialOptions{
		passphrase:     flags.String("passphrase", "", "repository passphrase (least safe: visible in shell history/ps; prefer -passphrase-file or BACKUP_ENGINE_PASSPHRASE)"),
		passphraseFile: flags.String("passphrase-file", "", "path to a file containing the repository passphrase"),
		salt:           flags.String("salt", "", "repository salt, min 16 chars (least safe: visible in shell history/ps; prefer -salt-file or BACKUP_ENGINE_SALT)"),
		saltFile:       flags.String("salt-file", "", "path to a file containing the repository salt"),
	}
}

func (o *credentialOptions) resolvePassphrase() ([]byte, error) {
	return resolveCredential(*o.passphrase, *o.passphraseFile, "BACKUP_ENGINE_PASSPHRASE", "passphrase")
}

func (o *credentialOptions) resolveSalt() ([]byte, error) {
	return resolveCredential(*o.salt, *o.saltFile, "BACKUP_ENGINE_SALT", "salt")
}

func resolveCredential(flagValue, filePath, envVar, label string) ([]byte, error) {
	if flagValue != "" {
		return []byte(flagValue), nil
	}
	if filePath != "" {
		raw, err := os.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("read %s file: %w", label, err)
		}
		return []byte(strings.TrimSpace(string(raw))), nil
	}
	if value := os.Getenv(envVar); value != "" {
		return []byte(value), nil
	}
	return nil, nil
}
