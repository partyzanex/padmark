package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"

	urcli "github.com/urfave/cli/v3"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

func shortenCommand() *urcli.Command {
	return &urcli.Command{
		Name:      "shorten",
		Usage:     "Create a new short link",
		ArgsUsage: " ",
		Flags: []urcli.Flag{
			&urcli.StringFlag{
				Name:     FlagTargetURL,
				Required: true,
				Usage:    "Destination URL (http/https only)",
			},
			&urcli.StringFlag{
				Name:  FlagSlug,
				Usage: "Custom URL slug (letters, digits, hyphens, underscores; single segment — links are served at /r/{id})",
			},
			&urcli.StringFlag{
				Name:  FlagPrivacy,
				Usage: "Privacy level: public (default), authenticated, or owner",
			},
			&urcli.BoolFlag{
				Name:  FlagBurn,
				Usage: "Burn after click: delete the link the moment it is first followed",
			},
			&urcli.Int64Flag{
				Name:  FlagTTL,
				Usage: "Seconds until the link expires, counted from creation (omit for no expiry)",
			},
			&urcli.StringFlag{
				Name:    FlagEditCode,
				Aliases: []string{"e"},
				Usage:   "Custom edit code, 8-128 chars (random 12-char code generated if omitted). " + psWarnEditCode,
				Sources: urcli.EnvVars(EnvEditCode),
			},
			&urcli.BoolFlag{
				Name: FlagDirectRedirect,
				Usage: "Override the server's --direct-redirect default for this link only " +
					"(--direct-redirect=false forces the transparency interstitial); omitted inherits the server default",
			},
		},
		Action: shortenAction,
	}
}

func shortenAction(ctx context.Context, cmd *urcli.Command) error {
	req, err := buildShortenReq(cmd)
	if err != nil {
		return err
	}

	cl, err := newPadmarkClient(cmd)
	if err != nil {
		return err
	}

	res, err := cl.CreateLink(ctx, req)
	if err != nil {
		return fmt.Errorf("create link: %w", err)
	}

	link, apiErr := handleCreateLinkRes(res)
	if apiErr != nil {
		return apiErr
	}

	return printCreateLinkResult(os.Stdout, resolveServerURL(cmd), link)
}

func buildShortenReq(cmd *urcli.Command) (*padmark.CreateLinkRequest, error) {
	target, err := url.Parse(cmd.String(FlagTargetURL))
	if err != nil {
		return nil, fmt.Errorf("invalid --%s: %w", FlagTargetURL, err)
	}

	// Only an explicitly set non-positive TTL is a user error — an unset TTL legitimately means
	// "no expiry", and flag zero values must never fail.
	if ttl := cmd.Int64(FlagTTL); cmd.IsSet(FlagTTL) && ttl <= 0 {
		return nil, fmt.Errorf("invalid --%s: must be a positive number of seconds (omit for no expiry)", FlagTTL)
	}

	req := &padmark.CreateLinkRequest{
		TargetURL: *target,
		// Opt fields zeroed: the server applies its defaults. Only the flags the CLI explicitly
		// reads (--slug, --burn, --ttl, --edit-code, --privacy, --direct) populate these later.
		Slug:           padmark.OptString{Value: "", Set: false},
		BurnAfterClick: padmark.OptBool{Value: false, Set: false},
		TTL:            padmark.OptInt64{Value: 0, Set: false},
		EditCode:       padmark.OptString{Value: "", Set: false},
		Privacy:        padmark.OptCreateLinkRequestPrivacy{Value: "", Set: false},
		DirectRedirect: padmark.OptNilBool{Value: false, Set: false, Null: false},
	}

	if slug := cmd.String(FlagSlug); slug != "" {
		req.Slug = padmark.NewOptString(slug)
	}

	if p := cmd.String(FlagPrivacy); p != "" {
		req.Privacy = padmark.NewOptCreateLinkRequestPrivacy(padmark.CreateLinkRequestPrivacy(p))
	}

	if ec := cmd.String(FlagEditCode); ec != "" {
		req.EditCode = padmark.NewOptString(ec)
	}

	if cmd.Bool(FlagBurn) {
		req.BurnAfterClick = padmark.NewOptBool(true)
	}

	if ttl := cmd.Int64(FlagTTL); ttl > 0 {
		req.TTL = padmark.NewOptInt64(ttl)
	}

	if cmd.IsSet(FlagDirectRedirect) {
		req.DirectRedirect = padmark.NewOptNilBool(cmd.Bool(FlagDirectRedirect))
	}

	return req, nil
}

func handleCreateLinkRes(res padmark.CreateLinkRes) (*padmark.CreateLinkResponse, error) {
	switch typed := res.(type) {
	case *padmark.CreateLinkResponseHeaders:
		resp := typed.Response

		return &resp, nil
	case *padmark.CreateLinkBadRequest:
		return nil, fmt.Errorf("bad request: %s", typed.Message)
	case *padmark.CreateLinkUnauthorized:
		return nil, fmt.Errorf("unauthorized: %s", typed.Message)
	case *padmark.CreateLinkConflict:
		return nil, fmt.Errorf("slug conflict: %s", typed.Message)
	case *padmark.CreateLinkRequestEntityTooLarge:
		return nil, fmt.Errorf("request too large: %s", typed.Message)
	case *padmark.CreateLinkUnprocessableEntity:
		return nil, fmt.Errorf("validation error: %s", typed.Message)
	case *padmark.CreateLinkTooManyRequests:
		return nil, errors.New("rate limit exceeded, try again later")
	case *padmark.CreateLinkServiceUnavailable:
		return nil, serviceUnavailableError(padmark.ErrorResponse(*typed))
	case *padmark.CreateLinkInternalServerError:
		return nil, fmt.Errorf("server error: %s", typed.Message)
	default:
		return nil, fmt.Errorf("unexpected response type: %T", res)
	}
}
