// secret-helper is a Docker Desktop extension host binary that executes
// a user-provided shell command and prints its stdout. It is used by the
// LiteLLM extension to fetch secrets from external tools (Vault, AWS
// Secrets Manager, etc.) running on the host machine.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: secret-helper <command> [args...]")
		os.Exit(1)
	}

	command := strings.Join(os.Args[1:], " ")

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", command)
	} else {
		cmd = exec.Command("sh", "-c", command)
	}

	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Print trimmed output (the secret value) to stdout.
	fmt.Print(strings.TrimSpace(string(out)))
}
