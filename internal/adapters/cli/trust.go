package cli

import (
	"context"
	"fmt"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/clifactory"
	"github.com/partyzanex/padmark/internal/infra/tokenfile"
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
	_, embeddedURL := splitToken(resolveToken(cmd))

	serverURL := cmd.String(FlagURL)

	if !cmd.IsSet(FlagURL) {
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

	// The confirmation is the command's only output, so a failed write fails the command like
	// the other commands' output. The origin is already recorded, and a retry is idempotent.
	_, writeErr := fmt.Fprintf(cmd.Root().Writer, trustedOriginMsg, origin, path)
	if writeErr != nil {
		return fmt.Errorf("write trust confirmation: %w", writeErr)
	}

	return nil
}

// ensureTrustedServer enforces trust-on-first-use for the server URL a pmk_ envelope token
// pins: the embedded URL is attacker-controllable when the token comes from a third party,
// and requests would carry the note content, the Authorization header and edit codes to
// wherever it points. Only the case where the envelope actually decides needs confirmation —
// an explicit --url/PADMARK_URL always wins over the embedded URL and is its own confirmation,
// and a bare legacy token pins nothing.
func ensureTrustedServer(cmd *urcli.Command, embeddedURL, serverURL string) error {
	if embeddedURL == "" || cmd.IsSet(FlagURL) {
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
		"`padmark-cli trust` to record it, or choose "+
		"the server explicitly with --url/PADMARK_URL", ErrUntrustedServer, origin)
}

// resolveTrustedServer is the single gate every server-bound command goes through before any
// network I/O: it resolves the token once, splits it into the bearer key and any embedded URL,
// picks the server URL with the usual precedence and enforces ensureTrustedServer. It fails
// closed — on a trust error no URL is returned, so no request can be built for it.
func resolveTrustedServer(cmd *urcli.Command) (bearer, serverURL string, err error) {
	bearer, embeddedURL := splitToken(resolveToken(cmd))

	serverURL = pickServerURL(cmd, embeddedURL)

	trustErr := ensureTrustedServer(cmd, embeddedURL, serverURL)
	if trustErr != nil {
		return "", "", fmt.Errorf("validate trusted server: %w", trustErr)
	}

	return bearer, serverURL, nil
}
