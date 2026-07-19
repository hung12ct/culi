package cli

import (
	"context"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/mcpserver"
)

// MCP serves the stdio MCP server — spawned by Claude Code, runs until the
// client disconnects.
func MCP(args []string) error {
	base, err := config.BaseDir()
	if err != nil {
		return err
	}
	return mcpserver.Run(context.Background(), base)
}
