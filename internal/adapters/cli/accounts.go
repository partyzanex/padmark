package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/errorcontext"
	padmark "github.com/partyzanex/padmark/pkg/client"
)

// errStepUpNoTOTPCode: issuing with a bearer token always needs the step-up, so the CLI fails
// before sending a request the server would refuse anyway.
var errStepUpNoTOTPCode = errors.New("--" + FlagTOTPCode + " is required: issuing with an API token " +
	"needs a re-authentication (account password + current TOTP code)")

// stepUpFlags are the re-authentication inputs every credential-issuing command takes: the CLI
// always authenticates with a bearer token, which the server lets issue only with a step-up.
func stepUpFlags() []urcli.Flag {
	return []urcli.Flag{
		&urcli.StringFlag{
			Name: FlagPassword,
			Usage: "Account password for the step-up. Flag values are visible in process listings (ps); " +
				"prefer piping the password to stdin",
		},
		&urcli.StringFlag{
			Name:  FlagTOTPCode,
			Usage: "Current 6-digit TOTP code from your authenticator (step-up; each code works once)",
		},
	}
}

// readStepUp resolves the step-up: the TOTP code flag, and the password from --password or stdin.
func readStepUp(cmd *urcli.Command) (password, totpCode string, err error) {
	totpCode = strings.TrimSpace(cmd.String(FlagTOTPCode))
	if totpCode == "" {
		return "", "", errStepUpNoTOTPCode
	}

	password, err = setupPassword(cmd)
	if err != nil {
		return "", "", fmt.Errorf("setup password: %w", err)
	}

	return password, totpCode, nil
}

// createTokenCommand and createInviteCommand are intentionally symmetric: one-time secret,
// stderr advice, single-line stdout. ogen per-operation unions make shared extraction awkward.
func createTokenCommand() *urcli.Command {
	return &urcli.Command{
		Name:  "create-token",
		Usage: "Issue an API token for your own account and print it once",
		Description: "POST /api/tokens: the returned pmk_… envelope is shown exactly once — " +
			"put it into ~/.config/padmark/token or PADMARK_TOKEN yourself; the CLI never writes " +
			"the token file automatically. A user may hold at most 20 live tokens (revoke via /admin). " +
			"Issuing with an API token requires a step-up: the account password (stdin or --password) " +
			"and --totp-code. The new token is linked to the one used: revoking that one revokes it too.",
		Flags: append(stepUpFlags(), &urcli.IntFlag{
			Name:  FlagTTLDays,
			Usage: "Lifetime of the new token in days; 0 = never expires",
			Value: 0,
		}),
		Action: createTokenAction,
	}
}

func createTokenAction(ctx context.Context, cmd *urcli.Command) error {
	apiClient, err := newPadmarkClient(cmd)
	if err != nil {
		return fmt.Errorf("create API client: %w", err)
	}

	password, totpCode, err := readStepUp(cmd)
	if err != nil {
		return fmt.Errorf("read step up: %w", err)
	}

	res, clientErr := apiClient.CreateAPIToken(ctx, padmark.NewOptCreateAPITokenRequest(padmark.CreateAPITokenRequest{
		TTLDays:  padmark.NewOptInt(cmd.Int(FlagTTLDays)),
		Password: padmark.NewOptString(password),
		TotpCode: padmark.NewOptString(totpCode),
	}))
	if clientErr != nil {
		return fmt.Errorf("create token: %w", clientErr)
	}

	token, err := handleCreateAPITokenRes(res)
	if err != nil {
		return fmt.Errorf("handle create apitoken res: %w", err)
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
		return "", fmt.Errorf("%w: %s", ErrUnauthorized, typed.Message)
	case *padmark.CreateAPITokenForbidden:
		return "", fmt.Errorf("%w: %s", ErrStepUpRefused, typed.Message)
	case *padmark.CreateAPITokenUnprocessableEntity:
		return "", fmt.Errorf("%w: %s", ErrInvalidRequest, typed.Message)
	case *padmark.CreateAPITokenTooManyRequests:
		return "", fmt.Errorf("%w: %s", ErrTokenLimit, typed.Message)
	case *padmark.CreateAPITokenServiceUnavailable:
		return "", errorcontext.Wrap(
			serviceUnavailableError(padmark.ErrorResponse(*typed)), "decode create API token response",
		)
	case *padmark.CreateAPITokenInternalServerError:
		return "", fmt.Errorf("%w: %s", ErrServer, typed.Message)
	default:
		return "", fmt.Errorf("%w: %T", ErrUnexpectedResponse, res)
	}
}

func createInviteCommand() *urcli.Command {
	return &urcli.Command{
		Name:  "create-invite",
		Usage: "Issue a single-use signup invite link (admin only)",
		Description: "POST /api/invites: returns the invite URL shown once — the link expires " +
			"and is single-use. The invitee signs up with `padmark-cli setup --invite <token>`. " +
			"Issuing with an API token requires a step-up: the account password (stdin or --password) " +
			"and --totp-code.",
		Flags:  stepUpFlags(),
		Action: createInviteAction,
	}
}

func createInviteAction(ctx context.Context, cmd *urcli.Command) error {
	apiClient, err := newPadmarkClient(cmd)
	if err != nil {
		return fmt.Errorf("create API client: %w", err)
	}

	password, totpCode, err := readStepUp(cmd)
	if err != nil {
		return fmt.Errorf("read step up: %w", err)
	}

	res, clientErr := apiClient.CreateInvite(ctx, padmark.NewOptStepUpRequest(padmark.StepUpRequest{
		Password: padmark.NewOptString(password),
		TotpCode: padmark.NewOptString(totpCode),
	}))
	if clientErr != nil {
		return fmt.Errorf("create invite: %w", clientErr)
	}

	inviteURL, err := handleCreateInviteRes(res)
	if err != nil {
		return fmt.Errorf("handle create invite res: %w", err)
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
		return "", fmt.Errorf("%w: %s", ErrUnauthorized, typed.Message)
	case *padmark.CreateInviteForbidden:
		return "", fmt.Errorf("%w: %s", errAdminStepUpRequired, typed.Message)
	case *padmark.CreateInviteServiceUnavailable:
		return "", errorcontext.Wrap(serviceUnavailableError(padmark.ErrorResponse(*typed)), "decode create invite response")
	case *padmark.CreateInviteInternalServerError:
		return "", fmt.Errorf("%w: %s", ErrServer, typed.Message)
	default:
		return "", fmt.Errorf("%w: %T", ErrUnexpectedResponse, res)
	}
}
