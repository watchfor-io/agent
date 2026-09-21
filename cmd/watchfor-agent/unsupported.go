//go:build !linux

// watchfor-agent reads /proc and /sys: it is a Linux program. On any
// other system the binary builds, says so, and exits.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "watchfor-agent runs on Linux only")
	os.Exit(1)
}
