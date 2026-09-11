// Command dit is the operator CLI for dit: a stateless remote client for
// dit-server that holds only the server URL and the shared secret locally.
package main

import (
	"os"

	"github.com/andriotisnikos1/dit/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
