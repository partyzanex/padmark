package mcp

import (
	"context"
	"fmt"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/clifactory"
	"github.com/partyzanex/padmark/internal/infra/tokenfile"
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
	_, embeddedURL := splitToken(resolveToken(cmd))

	serverURL := cmd.String(FlagBaseURL)

	if !cmd.IsSet(FlagBaseURL) {
		if embeddedURL == "" {
			return errNoTrustTarget
		}

		serverURL = embeddedURL
	}

	origin, err := tokenfile.Origin(serverURL)
	if err != nil {
		return fmt.Errorf("resolve server URL: %w", err)
	}

	addErr := tokenfile.AddKnownOrigin(origin)
	if addErr != nil {
		return fmt.Errorf("record trusted server: %w", addErr)
	}

	path, pathErr := tokenfile.HostsPath()
	if pathErr != nil {
		path = "<config dir unavailable>"
	}

	// Advisory write to stderr; a failed message must not abort the success path.
	fmt.Fprintf(stderrWriter(cmd), trustedOriginMsg, origin, path) //nolint:errcheck

	return nil
}

// ensureTrustedServer enforces trust-on-first-use for the server URL a pmk_ envelope token
// pins: the embedded URL is attacker-controllable when the token comes from a third party,
// and requests would carry the note content, the Authorization header and edit codes to
// wherever it points. Only the case where the envelope actually decides needs confirmation —
// an explicit --base-url/PADMARK_URL always wins over the embedded URL and is its own
// confirmation, and a bare legacy token pins nothing.
func ensureTrustedServer(cmd *urcli.Command, embeddedURL, serverURL string) error {
	if embeddedURL == "" || cmd.IsSet(FlagBaseURL) {
		return nil
	}

	origin, err := tokenfile.Origin(serverURL)
	if err != nil {
		return fmt.Errorf("token pins an invalid server URL %q: %w", serverURL, err)
	}

	if tokenfile.IsKnownOrigin(origin) {
		return nil
	}

	return fmt.Errorf("%w %s, which is not trusted yet; run "+
		"`padmark-mcp trust` to record it, or choose "+
		"the server explicitly with "+
		"--base-url/PADMARK_URL", ErrUntrustedServer, origin)
}
