// Command culi is the context orchestrator for Claude Code and Codex: a canonical
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

const usage = `culi — context orchestrator for Claude Code and Codex

Usage:
  culi init [--harness=auto]    set up ~/.culi, register harness hooks + MCP
  culi hook <event>             hook handler (stdin JSON → stdout JSON; internal)
  culi mcp                      MCP stdio server (spawned by Claude Code; internal)
  culi index [--full]           sync knowledge/ into the index + embed changed cards
  culi query [--timing] <text>  debug retrieval from the terminal
  culi card list|show|rm <id>   inspect the card store
  culi down <id>                downvote a card (ranks lower, never removed)
  culi import scan|merge|apply  reconcile drifted .claude dirs into the store
  culi export [--check]         regenerate ~/.claude agents/skills from cards
  culi learn [--from-start] [--scan-codex]  mine queued/current or historical sessions
  culi review [--list]          approve/reject mined candidate cards
  culi gen [--repo X] [--target claude|codex|both]  git history → instructions + repo cards
  culi stats [--json]           token accounting, gate economics, learning spend
  culi doctor [--harness=codex] verify local harness wiring and recent activity
  culi serve [--addr host:port] local web review console (default localhost:7378)
  culi statusline               Claude Code statusLine segment (stdin JSON; internal)
  culi version                  print the running build's version + git commit
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
	case "learn":
		exit(cli.Learn(os.Args[2:]))
	case "review":
		exit(cli.Review(os.Args[2:]))
	case "gen":
		exit(cli.Gen(os.Args[2:]))
	case "stats":
		exit(cli.Stats(os.Args[2:]))
	case "doctor":
		exit(cli.Doctor(os.Args[2:]))
	case "serve":
		exit(cli.Serve(os.Args[2:]))
	case "statusline":
		exit(cli.Statusline(os.Args[2:]))
	case "version", "--version", "-v":
		exit(cli.Version(os.Args[2:]))
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
