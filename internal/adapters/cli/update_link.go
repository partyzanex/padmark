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

// updateLinkCommand changes a short link, as `edit` changes a note: the destination is
// replaced, and an omitted optional flag keeps its value.
func updateLinkCommand() *urcli.Command {
	return clifactory.Command(func(command *urcli.Command) {
		command.Name = "update-link"
		command.Usage = "Change a short link's destination and settings; an omitted flag keeps the value"
		command.ArgsUsage = argsUsageID
		command.Flags = []urcli.Flag{
			clifactory.StringFlag(func(flag *urcli.StringFlag) {
				flag.Name = FlagEditCode
				flag.Aliases = []string{"e"}
				flag.Sources = urcli.EnvVars(EnvEditCode)
				flag.Usage = "Edit code returned at link creation (env: PADMARK_EDIT_CODE). Optional if " +
					"--token identifies the link's owner. " + psWarnEditCode
			}),
			clifactory.StringFlag(func(flag *urcli.StringFlag) {
				flag.Name = FlagTargetURL
				flag.Required = true
				flag.Usage = "New destination URL (http/https only)"
			}),
			clifactory.StringFlag(func(flag *urcli.StringFlag) {
				flag.Name = FlagPrivacy
				flag.Usage = usagePrivacy
			}),
			clifactory.Int64Flag(func(flag *urcli.Int64Flag) {
				flag.Name = FlagTTL
				flag.Usage = "Seconds until the link expires, counted from this update"
			}),
			clifactory.BoolFlag(func(flag *urcli.BoolFlag) {
				flag.Name = FlagDirectRedirect
				flag.Usage = "Override the server's --direct-redirect default for this link " +
					"(--direct-redirect=false forces the transparency interstitial); omit to keep"
			}),
		}
		command.Action = updateLinkAction
	})
}

// updateLinkAction does not require --edit-code: the server also accepts the request when
// --token identifies the link's owner, as for `edit`.
func updateLinkAction(ctx context.Context, cmd *urcli.Command) error {
	id, err := recordIDArg(cmd, linkRecord())
	if err != nil {
		return fmt.Errorf("read link ID: %w", err)
	}

	req, err := buildUpdateLinkReq(cmd)
	if err != nil {
		return fmt.Errorf("build update link request: %w", err)
	}

	apiClient, _, err := clientauth.NewClient(clientSettings(cmd))
	if err != nil {
		return fmt.Errorf("create API client: %w", err)
	}

	callCtx, cancel := requestContext(ctx, cmd)
	defer cancel()

	res, err := apiClient.UpdateLink(callCtx, req, padmark.UpdateLinkParams{ID: id})
	if err != nil {
		return fmt.Errorf("update link: %w", err)
	}

	link, err := handleUpdateLinkRes(res)
	if err != nil {
		return fmt.Errorf("decode update link response: %w", err)
	}

	return errorcontext.Wrap(printLink(commandWriter(cmd), link), "print link")
}

// buildUpdateLinkReq sends the destination and only the optional fields of the flags that the
// user set. The server keeps the value of each omitted field.
func buildUpdateLinkReq(cmd *urcli.Command) (*padmark.UpdateLinkRequest, error) {
	target, err := url.Parse(cmd.String(FlagTargetURL))
	if err != nil {
		return nil, fmt.Errorf("invalid --%s: %w", FlagTargetURL, err)
	}

	ttl, ttlSet, err := ttlFlag(cmd)
	if err != nil {
		return nil, fmt.Errorf("validate TTL: %w", err)
	}

	req := &padmark.UpdateLinkRequest{
		TargetURL:      *target,
		EditCode:       optEditCode(cmd.String(FlagEditCode)),
		TTL:            padmark.OptNilInt64{Value: 0, Set: false, Null: false},
		Privacy:        padmark.OptUpdateLinkRequestPrivacy{Value: "", Set: false},
		DirectRedirect: padmark.OptNilBool{Value: false, Set: false, Null: false},
	}

	if ttlSet {
		req.TTL = padmark.NewOptNilInt64(ttl)
	}

	if privacy := cmd.String(FlagPrivacy); privacy != "" {
		req.Privacy = padmark.NewOptUpdateLinkRequestPrivacy(padmark.UpdateLinkRequestPrivacy(privacy))
	}

	if cmd.IsSet(FlagDirectRedirect) {
		req.DirectRedirect = padmark.NewOptNilBool(cmd.Bool(FlagDirectRedirect))
	}

	return req, nil
}

// handleUpdateLinkRes returns the updated link or the CLI error of the response.
func handleUpdateLinkRes(res padmark.UpdateLinkRes) (*padmark.LinkResponse, error) {
	link, ok := res.(*padmark.LinkResponse)
	if !ok {
		return nil, responseError(res, linkRecord(), mutationRules())
	}

	// The generated decoder leaves a "#fragment" in the path, and the output would show "%23".
	link.TargetURL = padmark.SplitFragment(link.TargetURL)

	return link, nil
}
