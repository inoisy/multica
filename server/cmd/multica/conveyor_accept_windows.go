//go:build windows

package main

import "os/exec"

// ownProcessGroup keeps the default kill on Windows, where acceptance is not
// expected to run.
func ownProcessGroup(cmd *exec.Cmd) {}
