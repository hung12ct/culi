// Command culi is the context orchestrator for Claude Code: a canonical
// knowledge-card store, token-budgeted context injection via hooks, and (in
// later phases) MCP retrieval and background learning.
//
// Dispatch only — real work lives in internal packages.
package main

import (
	"fmt"
	"os"

	"github.com/hung12ct/culi/internal/cli"
	"github.com/hung12ct/culi/internal/hook"
)

const usage = `culi — context orchestrator for Claude Code

Usage:
  culi init                     set up ~/.culi, register hooks + MCP server
  culi hook <event>             hook handler (stdin JSON → stdout JSON; internal)
  culi mcp                      MCP stdio server (spawned by Claude Code; internal)
  culi index [--full]           sync knowledge/ into the index + embed changed cards
  culi query [--timing] <text>  debug retrieval from the terminal
  culi card list|show|rm <id>   inspect the card store
  culi down <id>                downvote a card (ranks lower, never removed)
  culi import scan|merge|apply  reconcile drifted .claude dirs into the store
  culi export [--check]         regenerate ~/.claude agents/skills from cards
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "hook":
		// The hook path owns stdout and always exits 0 (fail-open).
		os.Exit(hook.Run(os.Args[2:], os.Stdin, os.Stdout))
	case "mcp":
		exit(cli.MCP(os.Args[2:]))
	case "init":
		exit(cli.Init(os.Args[2:]))
	case "down":
		exit(cli.Down(os.Args[2:]))
	case "index":
		exit(cli.Index(os.Args[2:]))
	case "query":
		exit(cli.Query(os.Args[2:]))
	case "card":
		exit(cli.Card(os.Args[2:]))
	case "import":
		exit(cli.Import(os.Args[2:]))
	case "export":
		exit(cli.Export(os.Args[2:]))
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "culi: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

func exit(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "culi: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}
