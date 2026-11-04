// A tiny MCP server for the shoe shop: the tools an agent would use to answer
// customers, served over stdio (the default) or streamable HTTP (--http).
// examples/mcp-server/suite.yaml tests it with the mcp target.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

var orders = map[string]map[string]any{
	"ORD-1001": {"id": "ORD-1001", "status": "shipped", "carrier": "DHL", "eta_days": 2, "items": []string{"Mochi Runner 42"}},
	"ORD-1002": {"id": "ORD-1002", "status": "delivered", "delivered_days_ago": 40, "items": []string{"Mochi Loafer 39"}},
}

var policies = map[string]string{
	"returns":  "Returns are accepted within 30 days of delivery. After 30 days there are no exceptions; a 10% voucher on the next order is offered instead.",
	"shipping": "Shipping takes 2 to 4 business days. Orders over 80 EUR ship free.",
	"warranty": "Manufacturing defects are covered for 2 years from delivery.",
}

func main() {
	httpAddr := flag.String("http", "", "serve streamable HTTP on this address instead of stdio (for example 127.0.0.1:8090)")
	flag.Parse()

	s := server.NewMCPServer("mochi-shoes", "1.0.0", server.WithToolCapabilities(false))
	s.AddTool(mcp.NewTool("lookup_policy",
		mcp.WithDescription("The shop policy for a topic: returns, shipping or warranty"),
		mcp.WithString("topic", mcp.Required(), mcp.Description("returns, shipping or warranty")),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		topic := strings.ToLower(req.GetString("topic", ""))
		text, ok := policies[topic]
		if !ok {
			return mcp.NewToolResultError(fmt.Sprintf("no policy for %q; topics are returns, shipping and warranty", topic)), nil
		}
		return mcp.NewToolResultText(jsonOf(map[string]any{"topic": topic, "policy": text})), nil
	})
	s.AddTool(mcp.NewTool("lookup_order",
		mcp.WithDescription("An order by its id, such as ORD-1001"),
		mcp.WithString("id", mcp.Required(), mcp.Description("The order id")),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := strings.ToUpper(req.GetString("id", ""))
		order, ok := orders[id]
		if !ok {
			return mcp.NewToolResultError(fmt.Sprintf("no order %q", id)), nil
		}
		return mcp.NewToolResultText(jsonOf(order)), nil
	})
	s.AddTool(mcp.NewTool("end_session",
		mcp.WithDescription("Call when the customer has no more questions"),
	), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(`{"ok":true}`), nil
	})

	if *httpAddr != "" {
		log.Printf("mcp-server listening on http://%s/mcp", *httpAddr)
		if err := server.NewStreamableHTTPServer(s).Start(*httpAddr); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := server.ServeStdio(s); err != nil {
		log.Fatal(err)
	}
}

func jsonOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
