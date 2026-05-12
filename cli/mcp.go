package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type MCPToolInfo struct {
	Name        string
	Description string
	Params      []MCPParamInfo
	HasBody     bool
	Handler     func(args map[string]interface{}) (interface{}, error)
}

type MCPParamInfo struct {
	Name        string
	Type        string
	Description string
	Required    bool
}

var mcpTools []MCPToolInfo

func RegisterMCPTool(info MCPToolInfo) {
	mcpTools = append(mcpTools, info)
}

func MCPBodyArg(args map[string]interface{}) string {
	body, ok := args["body"]
	if !ok {
		return ""
	}
	b, _ := json.Marshal(body)
	return string(b)
}

func buildPropertyOpts(p MCPParamInfo) []mcp.PropertyOption {
	pOpts := []mcp.PropertyOption{}
	if p.Description != "" {
		pOpts = append(pOpts, mcp.Description(p.Description))
	}
	if p.Required {
		pOpts = append(pOpts, mcp.Required())
	}
	return pOpts
}

func startMCPServer(cmd *cobra.Command, args []string) {
	verbose := viper.GetBool("verbose")
	transport, _ := cmd.Flags().GetString("transport")
	port, _ := cmd.Flags().GetInt("port")

	opts := []server.ServerOption{
		server.WithToolCapabilities(true),
	}

	if verbose {
		opts = append(opts, server.WithToolHandlerMiddleware(
			func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
				return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					start := time.Now()
					name := req.Params.Name
					argsJSON, _ := json.Marshal(req.Params.Arguments)
					fmt.Fprintf(Stderr, "[mcp] -> %s %s\n", name, string(argsJSON))

					result, err := next(ctx, req)

					elapsed := time.Since(start)
					if err != nil {
						fmt.Fprintf(Stderr, "[mcp] <- %s ERROR %v (%s)\n", name, err, elapsed)
					} else {
						isError := false
						if result != nil {
							isError = result.IsError
						}
						if isError {
							fmt.Fprintf(Stderr, "[mcp] <- %s FAIL (%s)\n", name, elapsed)
						} else {
							fmt.Fprintf(Stderr, "[mcp] <- %s OK (%s)\n", name, elapsed)
						}
					}
					return result, err
				}
			},
		))
	}

	s := server.NewMCPServer(
		Root.Name(),
		Root.Version,
		opts...,
	)

	for _, info := range mcpTools {
		toolOpts := []mcp.ToolOption{
			mcp.WithDescription(info.Description),
		}
		for _, p := range info.Params {
			pOpts := buildPropertyOpts(p)
			switch p.Type {
			case "string":
				toolOpts = append(toolOpts, mcp.WithString(p.Name, pOpts...))
			case "int64", "float64":
				toolOpts = append(toolOpts, mcp.WithNumber(p.Name, pOpts...))
			case "boolean":
				toolOpts = append(toolOpts, mcp.WithBoolean(p.Name, pOpts...))
			default:
				toolOpts = append(toolOpts, mcp.WithString(p.Name, pOpts...))
			}
		}
		if info.HasBody {
			toolOpts = append(toolOpts, mcp.WithObject("body",
				mcp.Description("Request body as a JSON object"),
			))
		}

		tool := mcp.NewTool(info.Name, toolOpts...)

		handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			arguments, ok := req.Params.Arguments.(map[string]interface{})
			if !ok {
				return mcp.NewToolResultError("invalid arguments type"), nil
			}
			result, err := info.Handler(arguments)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			resultJSON, err := json.Marshal(result)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("Failed to marshal result: %v", err)), nil
			}
			return mcp.NewToolResultText(string(resultJSON)), nil
		}

		s.AddTool(tool, handler)
	}

	fmt.Fprintf(Stderr, "[mcp] Registered %d tools\n", len(mcpTools))

	addr := fmt.Sprintf(":%d", port)

	switch transport {
	case "stdio", "":
		fmt.Fprintf(Stderr, "[mcp] Listening on stdio\n")
		if err := server.ServeStdio(s); err != nil {
			fmt.Fprintf(Stderr, "[mcp] Server error: %v\n", err)
		}
	case "streamable-http":
		httpServer := server.NewStreamableHTTPServer(s)
		fmt.Fprintf(Stderr, "[mcp] Listening on http://localhost%s/mcp (streamable-http)\n", addr)
		if err := httpServer.Start(addr); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(Stderr, "[mcp] Server error: %v\n", err)
		}
	case "sse":
		sseServer := server.NewSSEServer(s)
		fmt.Fprintf(Stderr, "[mcp] Listening on http://localhost%s/sse (legacy sse)\n", addr)
		if err := sseServer.Start(addr); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(Stderr, "[mcp] Server error: %v\n", err)
		}
	default:
		fmt.Fprintf(Stderr, "[mcp] Unknown transport: %s (supported: stdio, streamable-http, sse)\n", transport)
	}
}
