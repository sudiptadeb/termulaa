//go:build !darwin && !linux

package main

import "fmt"

// getCWD is a stub for unsupported platforms.
func getCWD(pid int) (string, error) {
	return "", fmt.Errorf("cwd tracking not supported on this platform")
}
