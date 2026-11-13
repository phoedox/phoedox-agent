// Command phoedox-agent runs on servers, VMs and LXC containers and reports
// to a Phoedox instance over an outbound WebSocket (mTLS).
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/phoedox/phoedox-agent/internal/collect"
)

var version = "dev"

const usage = `phoedox-agent – reports this host to Phoedox

Usage:
  phoedox-agent collect            Print the current snapshot as JSON
  phoedox-agent enroll <url> <token>  Register with an instance (not implemented yet)
  phoedox-agent run                Connect and serve approved jobs (not implemented yet)
  phoedox-agent version`

func main() {
	if len(os.Args) < 2 {
		fmt.Println(usage)
		return
	}
	switch os.Args[1] {
	case "collect":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(collect.Take())
	case "version":
		fmt.Println("phoedox-agent", version)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
}
