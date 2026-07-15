// Command mcp exposes the trace analyzer to AI agents as an MCP server over
// stdio, using the official Go MCP SDK. This file is provided complete — the
// point of the exercise is the analyzer; this is just the ~100 lines of
// plumbing that turn it into a tool an agent can call.
//
// Wire it into Claude Code (from this directory):
//
//	claude mcp add gotrace -- go -C "$(pwd)" run ./cmd/mcp
//
// (or build once with `go build -o /somewhere/gotrace-mcp ./cmd/mcp` and
// register the binary instead of `go run`.)
package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"traceagent/analyzer"
)

type analyzeArgs struct {
	Path   string `json:"path" jsonschema:"path to a Go execution trace (.trace) file on disk"`
	Format string `json:"format,omitempty" jsonschema:"output format: 'text' (default, compact tables) or 'json'"`
}

type topBlockingArgs struct {
	Path string `json:"path" jsonschema:"path to a Go execution trace (.trace) file on disk"`
	N    int    `json:"n,omitempty" jsonschema:"number of blocking sites to return (default 5)"`
}

func analyzeTrace(ctx context.Context, req *mcp.CallToolRequest, args analyzeArgs) (*mcp.CallToolResult, any, error) {
	s, err := analyzer.AnalyzeFile(args.Path)
	if err != nil {
		return nil, nil, err // the SDK reports this as a tool error
	}
	out := ""
	if args.Format == "json" {
		if out, err = s.JSON(); err != nil {
			return nil, nil, err
		}
	} else {
		out = s.Render(10)
	}
	return textResult(out), nil, nil
}

func topBlocking(ctx context.Context, req *mcp.CallToolRequest, args topBlockingArgs) (*mcp.CallToolResult, any, error) {
	s, err := analyzer.AnalyzeFile(args.Path)
	if err != nil {
		return nil, nil, err
	}
	n := args.N
	if n <= 0 {
		n = 5
	}
	var out strings.Builder
	out.WriteString(fmt.Sprintf("top %d blocking sites (of %d) over %v:\n", n, len(s.TopBlocking), s.TraceDuration))
	for i, b := range s.TopBlocking {
		if i >= n {
			break
		}
		out.WriteString(fmt.Sprintf("%2d. %s [%s] count=%d total=%v max=%v\n",
			i+1, b.Site, b.Reason, b.Count, b.Total, b.Max))
	}
	return textResult(out.String()), nil, nil
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}

func main() {
	server := mcp.NewServer(&mcp.Implementation{Name: "gotrace", Version: "v0.1.0"}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "analyze_trace",
		Description: "Summarize a Go execution trace: per-goroutine-group time in each " +
			"scheduling state (running/runnable/blocked/syscall), top blocking sites " +
			"with wait reasons, and the longest individual scheduler waits. " +
			"High 'runnable' time means scheduler starvation; high 'blocked' time " +
			"means contention or serialization.",
	}, analyzeTrace)

	mcp.AddTool(server, &mcp.Tool{
		Name: "top_blocking",
		Description: "List the top N source locations where goroutines blocked in a Go " +
			"execution trace, with wait reason, event count, and total/max wait time.",
	}, topBlocking)

	// stdio transport: the client (e.g. Claude Code) launches this process
	// and speaks JSON-RPC over stdin/stdout.
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
