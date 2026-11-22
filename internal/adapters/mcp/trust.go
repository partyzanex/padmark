package mcp

import (
	"context"
	"fmt"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/clientauth"
	"github.com/partyzanex/padmark/internal/clifactory"
)

// trustedOriginMsg is the success message of `trust`: what was recorded, and where.
const trustedOriginMsg = "trusted server %s (recorded in %s)\n"

// trustCommand is the padmark-mcp escape hatch for trust-on-first-use: the stdio server
// cannot ask questions (stdin carries the MCP protocol), so a fresh pmk_ envelope origin
// must be recorded by running `padmark-mcp trust` once — by a human or from a script.
// It writes to stderr only, keeping stdout clean even if invoked by mistake next to a
// protocol session.
func trustCommand() *urcli.Command {
	return clifactory.Command(func(command *urcli.Command) {
		command.Name = "trust"
		command.Usage = "Record the server pinned by the token (or --base-url) as trusted for pmk_ envelope tokens"
		command.Action = trustAction
	})
}

// trustAction resolves what to trust in the same precedence as request routing: an explicit
// --base-url/PADMARK_URL wins, otherwise the URL embedded in the token. With neither there is
// nothing to pin — trusting the built-in default would record a decision the user never made.
func trustAction(_ context.Context, cmd *urcli.Command) error {
	origin, path, err := clientauth.Trust(clientSettings(cmd))
	if err != nil {
		// The error of Trust is the complete message for the user, so no context is added.
		return fmt.Errorf("%w", err)
	}

	// The confirmation is the command's only output (stderr, because stdout belongs to the MCP
	// protocol), so a failed write fails the command. The origin is already recorded, and a retry
	// is idempotent.
	_, writeErr := fmt.Fprintf(stderrWriter(cmd), trustedOriginMsg, origin, path)
	if writeErr != nil {
		return fmt.Errorf("write trust confirmation: %w", writeErr)
	}

	return nil
}
