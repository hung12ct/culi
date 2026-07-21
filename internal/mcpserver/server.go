// Package mcpserver exposes the card store to Claude Code as an MCP stdio
// server — the pull side of culi. Three tools, deliberately no more:
// search_context (hybrid retrieval), expand_card (full body + attachments,
// records utility feedback), save_lesson (write-through to the store).
package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/embed"
	"github.com/hung12ct/culi/internal/store"
)

const serverVersion = "0.3.0"

const instructions = `culi serves the user's canonical knowledge store (rules, lessons, styles,
patterns, skills). Lines starting with ▸ in injected <ctx> blocks are pointers:
call expand_card with the 4-hex ID to pull the full content. Call search_context
when you need guidance that was not pushed. Call save_lesson when the user asks
to remember something durable.`

// Server owns the long-lived handles for one MCP session (Claude Code spawns
// one process per session).
type Server struct {
	base  string
	cfg   config.Config
	store *store.Store
	emb   embed.Embedder
}

// New opens the store and prepares the tool surface.
func New(ctx context.Context, base string) (*Server, error) {
	cfg, err := config.Load(base)
	if err != nil {
		return nil, fmt.Errorf("mcpserver: %w", err)
	}
	s, err := store.Open(ctx, config.DBPath(base))
	if err != nil {
		return nil, fmt.Errorf("mcpserver: %w", err)
	}
	return &Server{
		base:  base,
		cfg:   cfg,
		store: s,
		emb:   embed.NewOllama(cfg.Ollama.Endpoint, cfg.Ollama.Model),
	}, nil
}

// Close releases the store handle.
func (s *Server) Close() error { return s.store.Close() }

// MCP builds the configured protocol server.
func (s *Server) MCP() *mcp.Server {
	srv := mcp.NewServer(
		&mcp.Implementation{Name: "culi", Version: serverVersion},
		&mcp.ServerOptions{Instructions: instructions},
	)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "search_context",
		Description: "Search the user's canonical knowledge store (rules, lessons, styles, " +
			"patterns) with hybrid BM25+embedding retrieval. Returns card summaries with IDs; " +
			"use expand_card for full content.",
	}, s.searchContext)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "expand_card",
		Description: "Fetch a card's full body (and attachment file paths for skills) by card " +
			"ID or the 4-hex short ID shown after ▸ in injected context.",
	}, s.expandCard)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "save_lesson",
		Description: "Save a durable lesson card to the user's knowledge store. Use when the " +
			"user asks to remember a correction, decision, or hard-won fact for future sessions. " +
			"If culi already holds a closely-related lesson it will fold the new knowledge into " +
			"that card instead of duplicating — check the returned `merged`/`note` fields and tell " +
			"the user whether a new card was created or an existing one updated.",
	}, s.saveLesson)
	return srv
}

// Run serves stdio until the client disconnects — `culi mcp`.
func Run(ctx context.Context, base string) error {
	s, err := New(ctx, base)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	if err := s.MCP().Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("mcpserver: %w", err)
	}
	return nil
}
