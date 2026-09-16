package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/watchfor-io/agent/internal/update"
)

// runVerify is `watchfor-agent verify`: check a release's checksums.txt
// against its .minisig with the key built into this binary, then the
// sha256 of any files given. No network, no root — for install.sh right
// after the download, and for anyone who fetched a release by hand.
//
//	verify -checksums checksums.txt -sig checksums.txt.minisig watchfor-agent_0.5.0_linux_amd64.tar.gz
func runVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	checksumsPath := fs.String("checksums", "checksums.txt", "the release's checksums file")
	sigPath := fs.String("sig", "", "its minisign signature (default: <checksums>.minisig)")
	quiet := fs.Bool("q", false, "print nothing on success")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *sigPath == "" {
		*sigPath = *checksumsPath + ".minisig"
	}
	checksums, err := os.ReadFile(*checksumsPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "verify:", err)
		return exitError
	}
	sig, err := os.ReadFile(*sigPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "verify:", err)
		return exitError
	}
	comment, err := update.VerifyChecksums(checksums, sig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify: %s is NOT signed by the WatchFor release key: %v\n", filepath.Base(*checksumsPath), err)
		return exitError
	}
	if err := update.VerifyFiles(checksums, fs.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "verify:", err)
		return exitError
	}
	if !*quiet {
		fmt.Printf("signature ok: %s (%s)\n", filepath.Base(*checksumsPath), comment)
		for _, p := range fs.Args() {
			fmt.Printf("sha256 ok: %s\n", filepath.Base(p))
		}
	}
	return exitOK
}
