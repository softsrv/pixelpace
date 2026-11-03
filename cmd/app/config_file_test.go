package main

import (
	"os"
	"path/filepath"
	"testing"
)

// unsetEnv clears key for the test and restores it afterwards.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}

func writeConfigFile(t *testing.T, dir, contents string) string {
	t.Helper()
	path := filepath.Join(dir, "app.env")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigFileReadsConfigFile(t *testing.T) {
	unsetEnv(t, "PIXELPACE_TEST_FROM_FILE")
	t.Setenv("CONFIG_FILE", writeConfigFile(t, t.TempDir(), "PIXELPACE_TEST_FROM_FILE=from-file\n"))
	if err := loadConfigFile(); err != nil {
		t.Fatalf("loadConfigFile: %v", err)
	}
	if got := os.Getenv("PIXELPACE_TEST_FROM_FILE"); got != "from-file" {
		t.Fatalf("PIXELPACE_TEST_FROM_FILE = %q, want %q", got, "from-file")
	}
}

func TestLoadConfigFileEnvironmentWins(t *testing.T) {
	t.Setenv("PIXELPACE_TEST_OVERRIDE", "from-env")
	t.Setenv("CONFIG_FILE", writeConfigFile(t, t.TempDir(), "PIXELPACE_TEST_OVERRIDE=from-file\n"))
	if err := loadConfigFile(); err != nil {
		t.Fatalf("loadConfigFile: %v", err)
	}
	if got := os.Getenv("PIXELPACE_TEST_OVERRIDE"); got != "from-env" {
		t.Fatalf("PIXELPACE_TEST_OVERRIDE = %q, want the environment's %q", got, "from-env")
	}
}

func TestLoadConfigFileMissingNamedFileFails(t *testing.T) {
	t.Setenv("CONFIG_FILE", filepath.Join(t.TempDir(), "missing.env"))
	if err := loadConfigFile(); err == nil {
		t.Fatal("loadConfigFile with a missing CONFIG_FILE succeeded, want an error")
	}
}

func TestLoadConfigFileDefault(t *testing.T) {
	unsetEnv(t, "CONFIG_FILE")
	unsetEnv(t, "PIXELPACE_TEST_DEFAULT")
	dir := t.TempDir()
	t.Chdir(dir)

	if err := loadConfigFile(); err != nil {
		t.Fatalf("loadConfigFile without CONFIG_FILE or ./.env: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("PIXELPACE_TEST_DEFAULT=dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadConfigFile(); err != nil {
		t.Fatalf("loadConfigFile: %v", err)
	}
	if got := os.Getenv("PIXELPACE_TEST_DEFAULT"); got != "dotenv" {
		t.Fatalf("PIXELPACE_TEST_DEFAULT = %q, want %q from ./.env", got, "dotenv")
	}
}
