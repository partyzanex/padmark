package cli

import (
	"context"
	"fmt"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/clientauth"
	"github.com/partyzanex/padmark/internal/clifactory"
)

// trustedOriginMsg is the success message of `trust`: what was recorded, and where.
const trustedOriginMsg = "trusted server %s (recorded in %s)\n"

// trustCommand records the server pinned by a pmk_ envelope token as trusted. The act of
// running the command is the confirmation — no prompt is needed — so it also works headless
// (piped stdin, scripts) and is the escape hatch for MCP-only setups: padmark-mcp cannot ask
// questions over stdio, so a fresh envelope origin is trusted by running `trust` once.
func trustCommand() *urcli.Command {
	return clifactory.Command(func(command *urcli.Command) {
		command.Name = "trust"
		command.Usage = "Record the server pinned by the token (or --url) as trusted for pmk_ envelope tokens"
		command.Action = trustAction
	})
}

// trustAction resolves what to trust in the same precedence as request routing: an explicit
// --url/PADMARK_URL wins, otherwise the URL embedded in the token. With neither there is
// nothing to pin — trusting the built-in default would record a decision the user never made.
func trustAction(_ context.Context, cmd *urcli.Command) error {
	origin, path, err := clientauth.Trust(clientSettings(cmd))
	if err != nil {
		// The error of Trust is the complete message for the user, so no context is added.
		return fmt.Errorf("%w", err)
	}

	// The confirmation is the command's only output, so a failed write fails the command like
	// the other commands' output. The origin is already recorded, and a retry is idempotent.
	_, writeErr := fmt.Fprintf(cmd.Root().Writer, trustedOriginMsg, origin, path)
	if writeErr != nil {
		return fmt.Errorf("write trust confirmation: %w", writeErr)
	}

	return nil
}
