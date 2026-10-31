package mcp

import (
	"context"
	"errors"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

func checkServerTool() *mcpsdk.Tool {
	return newTool("check_server",
		"Check the configured padmark server's liveness and readiness. Returns live/ready flags and the error "+
			"text when a probe fails.",
		hints(true, false, false))
}

// checkServer runs both health probes. Both results are reported even when only one fails —
// masking one probe behind the other's error is what the padmark-cli ping path avoids.
func (m *Manager) checkServer(
	ctx context.Context, _ *mcpsdk.CallToolRequest, _ checkServerInput,
) (*mcpsdk.CallToolResult, checkServerOutput, error) {
	out := checkServerOutput{
		Live:  false,
		Ready: false,
		Error: "",
	}

	liveErr := m.client.Healthz(ctx)
	if liveErr != nil {
		m.log.DebugContext(ctx, "check_server: healthz failed", "err", liveErr)
	}

	readyErr := m.checkReadiness(ctx)

	out.Live, out.Ready = liveErr == nil, readyErr == nil
	out.Error = joinProbeErrors(liveErr, readyErr)

	return nil, out, nil
}

// checkReadiness mirrors padmark-cli's contract: a readyz 503 (ErrorResponse) is a readiness
// failure, not an unexpected response.
func (m *Manager) checkReadiness(ctx context.Context) error {
	res, err := m.client.Readyz(ctx)
	if err != nil {
		return fmt.Errorf("readyz: %w", err)
	}

	switch typed := res.(type) {
	case *padmark.ReadyzOK:
		return nil
	case *padmark.ErrorResponse:
		return fmt.Errorf("%w: %s", ErrServiceUnavailable, typed.Message)
	default:
		return fmt.Errorf("%w: %T", ErrUnexpectedResponse, res)
	}
}

// joinProbeErrors combines the two probe failures into one text; nil when the server is fine.
func joinProbeErrors(liveErr, readyErr error) string {
	return errorText(errors.Join(liveErr, readyErr))
}

func errorText(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}
