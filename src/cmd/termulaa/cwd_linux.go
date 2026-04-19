//go:build linux

package main

import (
	"fmt"
	"os"
)

// getCWD returns the current working directory of the given process on Linux
// by reading the /proc/<pid>/cwd symlink.
func getCWD(pid int) (string, error) {
	return os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
}
