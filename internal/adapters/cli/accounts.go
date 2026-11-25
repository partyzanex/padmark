package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/clientauth"
	"github.com/partyzanex/padmark/internal/clifactory"
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
		clifactory.StringFlag(func(flag *urcli.StringFlag) {
			flag.Name = FlagPassword
			flag.Usage = "Account password for the step-up. Flag values are visible in process listings (ps); " +
				"prefer piping the password to stdin"
		}),
		clifactory.StringFlag(func(flag *urcli.StringFlag) {
			flag.Name = FlagTOTPCode
			flag.Usage = "Current 6-digit TOTP code from your authenticator (step-up; each code works once)"
		}),
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
	return clifactory.Command(func(command *urcli.Command) {
		command.Name = "create-token"
		command.Usage = "Issue an API token for your own account and print it once"
		command.Description = "POST /api/tokens: the returned pmk_… envelope is shown exactly once — " +
			"put it into ~/.config/padmark/token or PADMARK_TOKEN yourself; the CLI never writes " +
			"the token file automatically. A user may hold at most 20 live tokens (revoke via /admin). " +
			"Issuing with an API token requires a step-up: the account password (stdin or --password) " +
			"and --totp-code. The new token is linked to the one used: revoking that one revokes it too."

		command.Flags = append(stepUpFlags(), clifactory.IntFlag(func(flag *urcli.IntFlag) {
			flag.Name = FlagTTLDays
			flag.Usage = "Lifetime of the new token in days; 0 = never expires"
			flag.Value = 0
		}))
		command.Action = createTokenAction
	})
}

func createTokenAction(ctx context.Context, cmd *urcli.Command) error {
	apiClient, _, err := clientauth.NewClient(clientSettings(cmd))
	if err != nil {
		return fmt.Errorf("create API client: %w", err)
	}

	password, totpCode, err := readStepUp(cmd)
	if err != nil {
		return fmt.Errorf("read step up: %w", err)
	}

	callCtx, cancel := requestContext(ctx, cmd)
	defer cancel()

	req := padmark.NewOptCreateAPITokenRequest(padmark.CreateAPITokenRequest{
		TTLDays:  padmark.NewOptInt(cmd.Int(FlagTTLDays)),
		Password: padmark.NewOptString(password),
		TotpCode: padmark.NewOptString(totpCode),
	})

	res, clientErr := apiClient.CreateAPIToken(callCtx, req)
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
	if typed, ok := res.(*padmark.CreateAPITokenResponse); ok {
		return typed.Token, nil
	}

	return "", responseError(res, accountRecord(), statusRules{
		http.StatusForbidden:           withMessage(ErrStepUpRefused),
		http.StatusUnprocessableEntity: withMessage(ErrInvalidRequest),
		http.StatusTooManyRequests:     withMessage(ErrTokenLimit),
	})
}

func createInviteCommand() *urcli.Command {
	return clifactory.Command(func(command *urcli.Command) {
		command.Name = "create-invite"
		command.Usage = "Issue a single-use signup invite link (admin only)"
		command.Description = "POST /api/invites: returns the invite URL shown once — the link expires " +
			"and is single-use. The invitee signs up with `padmark-cli setup --invite <token>`. " +
			"Issuing with an API token requires a step-up: the account password (stdin or --password) " +
			"and --totp-code."
		command.Flags = stepUpFlags()
		command.Action = createInviteAction
	})
}

func createInviteAction(ctx context.Context, cmd *urcli.Command) error {
	apiClient, _, err := clientauth.NewClient(clientSettings(cmd))
	if err != nil {
		return fmt.Errorf("create API client: %w", err)
	}

	password, totpCode, err := readStepUp(cmd)
	if err != nil {
		return fmt.Errorf("read step up: %w", err)
	}

	callCtx, cancel := requestContext(ctx, cmd)
	defer cancel()

	res, clientErr := apiClient.CreateInvite(callCtx, padmark.NewOptStepUpRequest(padmark.StepUpRequest{
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
	if typed, ok := res.(*padmark.CreateInviteResponse); ok {
		return typed.InviteURL.String(), nil
	}

	return "", responseError(res, accountRecord(), statusRules{
		http.StatusForbidden: withMessage(errAdminStepUpRequired),
	})
}
