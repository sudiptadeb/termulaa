//go:build darwin

package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// getCWD returns the current working directory of the given process on macOS
// by parsing lsof output for the "cwd" file descriptor.
func getCWD(pid int) (string, error) {
	out, err := exec.Command("lsof", "-p", fmt.Sprintf("%d", pid), "-Fn").Output()
	if err != nil {
		return "", err
	}

	lines := strings.Split(string(out), "\n")
	for i, line := range lines {
		if line == "fcwd" && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "n") {
			return lines[i+1][1:], nil
		}
	}
	return "", fmt.Errorf("cwd not found for pid %d", pid)
}
