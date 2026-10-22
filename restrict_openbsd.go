//go:build openbsd

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// restrictProcess pledges puffy after setup (the sqlite-vector extension
// is already loaded, so prot_exec is no longer needed). The promises cover
// what the loop does: read and write files, talk HTTPS to the model, ask
// on the terminal, and fork/exec approved scripts and llama-server.
// Children are not pledged: execpromises stays unset, because an approved
// script must be able to run anything the user could, such as doas.
// See pledge(2).
func restrictProcess(s Settings) {
	promises := "stdio rpath wpath cpath dpath fattr chown flock tty " +
		"proc exec inet dns"
	if err := unix.PledgePromises(promises); err != nil {
		fmt.Fprintf(os.Stderr, "puffy: pledge: %v\n", err)
	}
}
