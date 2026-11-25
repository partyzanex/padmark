package cli

import (
	"context"
	"fmt"
	"net/url"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/clientauth"
	"github.com/partyzanex/padmark/internal/clifactory"
	"github.com/partyzanex/padmark/internal/errorcontext"
	padmark "github.com/partyzanex/padmark/pkg/client"
)

func shortenCommand() *urcli.Command {
	return clifactory.Command(func(command *urcli.Command) {
		command.Name = "shorten"
		command.Usage = "Create a new short link"
		command.ArgsUsage = " "
		command.Flags = []urcli.Flag{
			clifactory.StringFlag(func(flag *urcli.StringFlag) {
				flag.Name = FlagTargetURL
				flag.Required = true
				flag.Usage = "Destination URL (http/https only)"
			}),
			clifactory.StringFlag(func(flag *urcli.StringFlag) {
				flag.Name = FlagSlug
				flag.Usage = "Custom URL slug (letters, digits, hyphens, underscores; " +
					"single segment — links are served at /{id})"
			}),
			clifactory.StringFlag(func(flag *urcli.StringFlag) {
				flag.Name = FlagPrivacy
				flag.Usage = usagePrivacyDefaultPublic
			}),
			clifactory.BoolFlag(func(flag *urcli.BoolFlag) {
				flag.Name = FlagBurn
				flag.Usage = "Burn after click: delete the link the moment it is first followed"
			}),
			clifactory.Int64Flag(func(flag *urcli.Int64Flag) {
				flag.Name = FlagTTL
				flag.Usage = "Seconds until the link expires, counted from creation (omit for no expiry)"
			}),
			clifactory.StringFlag(func(flag *urcli.StringFlag) {
				flag.Name = FlagEditCode
				flag.Aliases = []string{"e"}
				flag.Usage = "Custom edit code, 8-128 chars (random 12-char code generated if omitted). " +
					psWarnEditCode
				flag.Sources = urcli.EnvVars(EnvEditCode)
			}),
			clifactory.BoolFlag(func(flag *urcli.BoolFlag) {
				flag.Name = FlagDirectRedirect
				flag.Usage = "Override the server's --direct-redirect default for this link only " +
					"(--direct-redirect=false forces the transparency interstitial); " +
					"omitted inherits the server default"
			}),
		}
		command.Action = shortenAction
	})
}

func shortenAction(ctx context.Context, cmd *urcli.Command) error {
	req, err := buildShortenReq(cmd)
	if err != nil {
		return fmt.Errorf("build shorten req: %w", err)
	}

	apiClient, server, err := clientauth.NewClient(clientSettings(cmd))
	if err != nil {
		return fmt.Errorf("create API client: %w", err)
	}

	callCtx, cancel := requestContext(ctx, cmd)
	defer cancel()

	res, err := apiClient.CreateLink(callCtx, req)
	if err != nil {
		return fmt.Errorf("create link: %w", err)
	}

	link, apiErr := handleCreateLinkRes(res)
	if apiErr != nil {
		return fmt.Errorf("decode create link response: %w", apiErr)
	}

	return errorcontext.Wrap(
		printCreateLinkResult(commandWriter(cmd), server.URL, link), "print create link result")
}

func buildShortenReq(cmd *urcli.Command) (*padmark.CreateLinkRequest, error) {
	target, err := url.Parse(cmd.String(FlagTargetURL))
	if err != nil {
		return nil, fmt.Errorf("invalid --%s: %w", FlagTargetURL, err)
	}

	ttl, ttlSet, err := ttlFlag(cmd)
	if err != nil {
		return nil, fmt.Errorf("validate TTL: %w", err)
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

	if ttlSet {
		req.TTL = padmark.NewOptInt64(ttl)
	}

	if cmd.IsSet(FlagDirectRedirect) {
		req.DirectRedirect = padmark.NewOptNilBool(cmd.Bool(FlagDirectRedirect))
	}

	return req, nil
}

func handleCreateLinkRes(res padmark.CreateLinkRes) (*padmark.CreateLinkResponse, error) {
	typed, ok := res.(*padmark.CreateLinkResponseHeaders)
	if !ok {
		return nil, responseError(res, linkRecord(), nil)
	}

	resp := typed.Response
	// The generated decoder leaves a "#fragment" in the path, and the output would show "%23".
	resp.TargetURL = padmark.SplitFragment(resp.TargetURL)

	return &resp, nil
}
