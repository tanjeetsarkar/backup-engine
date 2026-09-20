package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveCredentialPrecedence(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(filePath, []byte("from-file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BE_TEST_CRED", "from-env")

	value, err := resolveCredential("from-flag", filePath, "BE_TEST_CRED", "test")
	if err != nil || string(value) != "from-flag" {
		t.Fatalf("flag precedence: got %q, err %v", value, err)
	}

	value, err = resolveCredential("", filePath, "BE_TEST_CRED", "test")
	if err != nil || string(value) != "from-file" {
		t.Fatalf("file precedence: got %q, err %v", value, err)
	}

	value, err = resolveCredential("", "", "BE_TEST_CRED", "test")
	if err != nil || string(value) != "from-env" {
		t.Fatalf("env fallback: got %q, err %v", value, err)
	}

	t.Setenv("BE_TEST_CRED", "")
	value, err = resolveCredential("", "", "BE_TEST_CRED", "test")
	if err != nil || value != nil {
		t.Fatalf("no source: got %q, err %v", value, err)
	}

	if _, err := resolveCredential("", filepath.Join(dir, "missing.txt"), "BE_TEST_CRED", "test"); err == nil {
		t.Fatal("expected error for missing credential file")
	}
}

func TestBindCredentialOptionsFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	creds := bindCredentialOptions(fs)
	if err := fs.Parse([]string{"-passphrase", "secret", "-salt", "0123456789abcdef"}); err != nil {
		t.Fatal(err)
	}
	passphrase, err := creds.resolvePassphrase()
	if err != nil || string(passphrase) != "secret" {
		t.Fatalf("passphrase = %q, err %v", passphrase, err)
	}
	salt, err := creds.resolveSalt()
	if err != nil || string(salt) != "0123456789abcdef" {
		t.Fatalf("salt = %q, err %v", salt, err)
	}
}
