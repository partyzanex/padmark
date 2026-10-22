package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	urcli "github.com/urfave/cli/v3"
)

func setupCommand() *urcli.Command {
	return &urcli.Command{
		Name:  "setup",
		Usage: "Create the first admin account (or enroll via an invite) and print TOTP URI",
		Description: "Headless account bootstrap: POSTs JSON to the server's /setup and prints " +
			"the otpauth:// enrollment URI exactly once — scan it or save it, it cannot be " +
			"recovered. Pass --username and --password; the password may also be piped to stdin " +
			"(first line). Bootstrapping the first admin requires the server's bootstrap token in " +
			"the " + EnvBootstrapToken + " environment variable. A non-empty --invite enrolls a new " +
			"(non-admin) user instead and needs no bootstrap token.",
		Flags: []urcli.Flag{
			&urcli.StringFlag{
				Name:  FlagUsername,
				Usage: "Account username",
			},
			&urcli.StringFlag{
				Name: FlagPassword,
				Usage: "Account password (strength rules enforced by the server). " +
					"Flag values are visible in process listings (ps); prefer piping the password to stdin",
			},
			&urcli.StringFlag{
				Name:    FlagInvite,
				Aliases: []string{"i"},
				Usage: "Signup invite token (admin-created); empty = bootstrap the first admin. " +
					"Flag values are visible in process listings (ps) and there is no env/stdin alternative " +
					"(stdin carries the password); the invite is single-use and this request spends it",
			},
		},
		Action: setupAction,
	}
}

func setupAction(ctx context.Context, cmd *urcli.Command) error {
	username := cmd.String(FlagUsername)
	if strings.TrimSpace(username) == "" {
		return errors.New("--username is required")
	}

	// The trust gate runs before stdin is consumed, before the insecure warning and before any
	// network I/O: a pmk_ envelope URL must not receive the bootstrap credentials unconfirmed.
	// Setup is unauthenticated, so the bearer key itself is deliberately not sent.
	_, serverURL, trustErr := resolveTrustedServer(cmd)
	if trustErr != nil {
		return trustErr
	}

	password, err := setupPassword(cmd)
	if err != nil {
		return err
	}

	invite := cmd.String(FlagInvite)

	// The bootstrap token only matters for the first admin; an invite is its own credential.
	var bootstrapToken string
	if invite == "" {
		bootstrapToken = os.Getenv(EnvBootstrapToken)
	}

	body := struct {
		Username       string `json:"username"`
		Password       string `json:"password"`
		Invite         string `json:"invite,omitempty"`
		BootstrapToken string `json:"bootstrap_token,omitempty"`
	}{username, password, invite, bootstrapToken}

	//nolint:gosec // G117: the password must reach the server; transport secrecy is HTTPS job
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode setup request: %w", err)
	}

	endpoint := serverURL + "/setup"

	// Same policy as the token flow: secrets must not travel in cleartext silently.
	insecureErr := checkInsecureSetup(commandErrWriter(cmd), endpoint, cmd.Bool(FlagAllowInsecureHTTP))
	if insecureErr != nil {
		return insecureErr
	}

	httpClient := &http.Client{Timeout: cmd.Duration(FlagTimeout)}

	req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if reqErr != nil {
		return fmt.Errorf("build setup request: %w", reqErr)
	}

	setupJSONHeaders(req)

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("setup request: %w", err)
	}

	// Body-close failure cannot change the command outcome; the config reports blank assigns.
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // see the comment above

	return handleSetupRes(commandErrWriter(cmd), resp)
}

// checkInsecureSetup guards the initial admin's password (and the returned TOTP secret, an
// equivalent credential) against cleartext transport with the same policy as checkInsecureToken.
func checkInsecureSetup(errOut io.Writer, serverURL string, allowInsecure bool) error {
	return checkInsecureTransport(errOut, serverURL, "the initial admin password and TOTP secret", allowInsecure)
}

// setupJSONHeaders marks the request as the machine-readable setup variant: application/json
// body and an explicit Accept that pins the server's negotiate() gate to the JSON branch even
// if an intermediary rewrites */*.
func setupJSONHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
}

// errSetupNoPassword — headless setup requires --password or a piped stdin; a visible-echo
// interactive prompt is rejected rather than offered (test pins the sentinel).
var errSetupNoPassword = errors.New(
	"--password is required (or pipe the password to stdin; a TTY prompt would echo it)")

// setupPassword resolves the setup password: the --password flag first, then the first line of
// piped stdin (headless provisioning without a shell-history leak), and errSetupNoPassword when
// an interactive TTY would force visible-echo input — that is a footgun, not a convenience.
func setupPassword(cmd *urcli.Command) (string, error) {
	if pass := cmd.String(FlagPassword); pass != "" {
		return pass, nil
	}

	stat, statErr := os.Stdin.Stat()
	if statErr == nil && isCharDevice(stat.Mode()) {
		return "", errSetupNoPassword
	}

	reader := bufio.NewReader(os.Stdin)

	line, readErr := reader.ReadString('\n')
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return "", fmt.Errorf("read password from stdin: %w", readErr)
	}

	return strings.TrimRight(line, "\r\n"), nil
}

// handleSetupRes consumes the /setup JSON response: 201 must surface the otpauth URI on stdout
// (single line, pipe-safe), warnings go to stderr.
func handleSetupRes(errOut io.Writer, resp *http.Response) error {
	if resp.StatusCode != http.StatusCreated {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		if readErr != nil {
			return fmt.Errorf("read setup response: %w", readErr)
		}

		var apiErr struct {
			Message string `json:"message"`
		}

		parseErr := json.Unmarshal(body, &apiErr)
		if parseErr != nil || apiErr.Message == "" {
			return fmt.Errorf("setup failed: HTTP %d", resp.StatusCode)
		}

		return fmt.Errorf("setup failed: %s", apiErr.Message)
	}

	var out struct {
		OTPAuthURI string `json:"otp_auth_uri"`
	}

	decErr := json.NewDecoder(resp.Body).Decode(&out)
	if decErr != nil {
		return fmt.Errorf("decode setup response: %w", decErr)
	}

	ew := &errWriter{w: errOut, err: nil}
	ew.printf("TOTP URI (and its secret) is shown exactly once — scan it now or save it.\n")

	if out.OTPAuthURI == "" {
		return errors.New("setup succeeded but the response carries no otpauth_uri")
	}

	// Single-line stdout print: piped-safe, machine-consumable.
	outw := &errWriter{w: os.Stdout, err: nil}
	outw.printf("%s\n", out.OTPAuthURI)

	if outw.err != nil {
		return fmt.Errorf("write output: %w", outw.err)
	}

	return nil
}

// errorBodyLimit caps how much of an error body is surfaced to the user.
const errorBodyLimit = 4096
