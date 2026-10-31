package cli

import (
	"io"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/tokenfile"
)

// resolveToken returns the bearer token in precedence order: the --token flag or PADMARK_TOKEN
// env (both surfaced through cmd.String), then the token config file ~/.config/padmark/token.
// The token is issued by an admin through /admin and copied into the file by the user; the CLI
// only reads it. A missing or unreadable file yields an empty token — auth is simply omitted.
func resolveToken(cmd *urcli.Command) string {
	if token := cmd.String(FlagToken); token != "" {
		return token
	}

	return readTokenFile(commandErrWriter(cmd))
}

// readTokenFile delegates to internal/infra/tokenfile — the padmark-mcp binary resolves the
// token file through the same code path, so file location and the 0600 invariant cannot drift
// between the two adapters.
func readTokenFile(errOut io.Writer) string {
	return tokenfile.Read(errOut)
}

// splitToken separates a configured token value into the bearer key sent to the server and any
// server base URL embedded in it. It is the CLI-side counterpart of
// domain.EncodeAPITokenEnvelope: an envelope token yields its key and URL, while a legacy bare
// key yields itself as the bearer and an empty URL.
func splitToken(raw string) (bearer, baseURL string) {
	if url, key, ok := domain.DecodeAPITokenEnvelope(raw); ok {
		return key, url
	}

	return raw, ""
}

// pickServerURL chooses the base server URL: an explicit --url/PADMARK_URL wins, otherwise the URL
// embedded in an envelope token, otherwise the --url default.
func pickServerURL(cmd *urcli.Command, embeddedURL string) string {
	if embeddedURL != "" && !cmd.IsSet(FlagURL) {
		return embeddedURL
	}

	return cmd.String(FlagURL)
}

// resolveServerURL returns the base server URL the client will actually use — for display in
// ping/create output as well as for requests — so what the user sees matches where requests go.
func resolveServerURL(cmd *urcli.Command) string {
	_, embeddedURL := splitToken(resolveToken(cmd))

	return pickServerURL(cmd, embeddedURL)
}
