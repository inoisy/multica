package repocache

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain runs git with a config of the tests' own: a developer's global
// core.hooksPath makes git skip the hooks this package installs into
// .git/hooks, and the Co-authored-by hook tests then fail on that machine
// only. Tests that need their own global config still set it themselves.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "repocache-gitconfig-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	global := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(global, []byte("[user]\n\tname = Repocache Test\n\temail = repocache-test@example.com\n"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("GIT_CONFIG_GLOBAL", global)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
