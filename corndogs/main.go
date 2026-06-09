package main

import "github.com/CatalystCommunity/corndogs/corndogs/cmd"

// main runs the corndogs CLI. The default "run" subcommand starts the
// CBOR-over-HTTP server (health on /healthz).
func main() {
	cmd.Execute()
}
