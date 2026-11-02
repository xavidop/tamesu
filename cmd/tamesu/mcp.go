package main

import (
	"context"

	"github.com/xavidop/tamesu/internal/cli"
	"github.com/xavidop/tamesu/llm"
)

// serveMCP is `tamesu mcp`: the tamesu tools over stdio on Genkit's MCP server.
func serveMCP(ctx context.Context, req cli.MCPRequest) error {
	return llm.ServeMCP(ctx, llm.MCPOptions{Version: req.Version, Exec: req.Exec, Schema: req.Schema, Logger: req.Logger})
}
