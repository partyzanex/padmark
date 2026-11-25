package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// limitCall adds the --timeout deadline to the context of each call of handler. The HTTP
// client has no total timeout, because a total timeout would also stop a large upload. Thus
// this deadline limits the ordinary tools: all requests of the call, the response bodies and
// their decode. upload_file does not use it, and only the response header timeout of the HTTP
// client limits an upload. The server reads callTimeout at call time; zero adds no deadline.
func limitCall[In, Out any](proxy *Server, handler mcpsdk.ToolHandlerFor[In, Out]) mcpsdk.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, req *mcpsdk.CallToolRequest, input In) (*mcpsdk.CallToolResult, Out, error) {
		if proxy.callTimeout <= 0 {
			return handler(ctx, req, input)
		}

		callCtx, cancel := context.WithTimeout(ctx, proxy.callTimeout)
		defer cancel()

		return handler(callCtx, req, input)
	}
}
