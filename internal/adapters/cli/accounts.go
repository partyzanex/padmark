package cli

import (
	"context"
	"fmt"
	"os"

	urcli "github.com/urfave/cli/v3"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// createTokenCommand and createInviteCommand are intentionally symmetric: one-time secret,
// stderr advice, single-line stdout. ogen per-operation unions make shared extraction awkward.
func createTokenCommand() *urcli.Command {
	return &urcli.Command{
		Name:  "create-token",
		Usage: "Issue an API token for your own account and print it once",
		Description: "POST /api/tokens: the returned pmk_… envelope is shown exactly once — " +
			"put it into ~/.config/padmark/token or PADMARK_TOKEN yourself; the CLI never writes " +
			"the token file automatically. A user may hold at most 20 live tokens (revoke via /admin).",
		Action: createTokenAction,
	}
}

//nolint:dupl // deliberately symmetric (see file comment)
func createTokenAction(ctx context.Context, cmd *urcli.Command) error {
	cl, err := newPadmarkClient(cmd)
	if err != nil {
		return err
	}

	res, clientErr := cl.CreateAPIToken(ctx)
	if clientErr != nil {
		return fmt.Errorf("create token: %w", clientErr)
	}

	token, err := handleCreateAPITokenRes(res)
	if err != nil {
		return err
	}

	// Keep the machine-readable value isolated on stdout; the warning goes to stderr.
	ew := &errWriter{w: commandErrWriter(cmd), err: nil}
	ew.printf("Token is shown exactly once — store it now (~/.config/padmark/token or PADMARK_TOKEN).\n")

	if ew.err != nil {
		return fmt.Errorf("write output: %w", ew.err)
	}

	outw := &errWriter{w: os.Stdout, err: nil}
	outw.printf("%s\n", token)

	if outw.err != nil {
		return fmt.Errorf("write output: %w", outw.err)
	}

	return nil
}

func handleCreateAPITokenRes(res padmark.CreateAPITokenRes) (string, error) {
	switch typed := res.(type) {
	case *padmark.CreateAPITokenResponse:
		return typed.Token, nil
	case *padmark.CreateAPITokenUnauthorized:
		return "", fmt.Errorf("unauthorized: %s", typed.Message)
	case *padmark.CreateAPITokenTooManyRequests:
		return "", fmt.Errorf("token cap reached (revoke one via /admin): %s", typed.Message)
	case *padmark.CreateAPITokenServiceUnavailable:
		return "", fmt.Errorf("feature not enabled: %s", typed.Message)
	case *padmark.CreateAPITokenInternalServerError:
		return "", fmt.Errorf("server error: %s", typed.Message)
	default:
		return "", fmt.Errorf("unexpected response type: %T", res)
	}
}

func createInviteCommand() *urcli.Command {
	return &urcli.Command{
		Name:  "create-invite",
		Usage: "Issue a single-use signup invite link (admin only)",
		Description: "POST /api/invites: returns the invite URL shown once — the link expires " +
			"and is single-use. The invitee signs up with `padmark-cli setup --invite <token>`.",
		Action: createInviteAction,
	}
}

//nolint:dupl // deliberately symmetric (see file comment)
func createInviteAction(ctx context.Context, cmd *urcli.Command) error {
	cl, err := newPadmarkClient(cmd)
	if err != nil {
		return err
	}

	res, clientErr := cl.CreateInvite(ctx)
	if clientErr != nil {
		return fmt.Errorf("create invite: %w", clientErr)
	}

	inviteURL, err := handleCreateInviteRes(res)
	if err != nil {
		return err
	}

	ew := &errWriter{w: commandErrWriter(cmd), err: nil}
	ew.printf("Single-use invite valid for 24h — send it to the invitee.\n")

	if ew.err != nil {
		return fmt.Errorf("write output: %w", ew.err)
	}

	outw := &errWriter{w: os.Stdout, err: nil}
	outw.printf("%s\n", inviteURL)

	if outw.err != nil {
		return fmt.Errorf("write output: %w", outw.err)
	}

	return nil
}

func handleCreateInviteRes(res padmark.CreateInviteRes) (string, error) {
	switch typed := res.(type) {
	case *padmark.CreateInviteResponse:
		return typed.InviteURL.String(), nil
	case *padmark.CreateInviteUnauthorized:
		return "", fmt.Errorf("unauthorized: %s", typed.Message)
	case *padmark.CreateInviteForbidden:
		return "", fmt.Errorf("admin required: %s", typed.Message)
	case *padmark.CreateInviteServiceUnavailable:
		return "", fmt.Errorf("feature not enabled: %s", typed.Message)
	case *padmark.CreateInviteInternalServerError:
		return "", fmt.Errorf("server error: %s", typed.Message)
	default:
		return "", fmt.Errorf("unexpected response type: %T", res)
	}
}
