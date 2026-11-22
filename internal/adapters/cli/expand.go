package cli

import (
	"context"
	"fmt"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/clientauth"
	"github.com/partyzanex/padmark/internal/clifactory"
	"github.com/partyzanex/padmark/internal/errorcontext"
	padmark "github.com/partyzanex/padmark/pkg/client"
)

func expandCommand() *urcli.Command {
	return clifactory.Command(func(command *urcli.Command) {
		command.Name = "expand"
		command.Usage = "Get a short link's metadata by ID"
		command.ArgsUsage = argsUsageID
		command.Flags = []urcli.Flag{
			clifactory.BoolFlag(func(flag *urcli.BoolFlag) {
				flag.Name = FlagJSON
				flag.Usage = "Print the link as JSON"
			}),
		}
		command.Action = expandAction
	})
}

func expandAction(ctx context.Context, cmd *urcli.Command) error {
	id, err := recordIDArg(cmd, linkRecord())
	if err != nil {
		return fmt.Errorf("read link ID: %w", err)
	}

	apiClient, _, err := clientauth.NewClient(clientSettings(cmd))
	if err != nil {
		return fmt.Errorf("create API client: %w", err)
	}

	callCtx, cancel := requestContext(ctx, cmd)
	defer cancel()

	res, err := apiClient.GetLink(callCtx, padmark.GetLinkParams{ID: id})
	if err != nil {
		return fmt.Errorf("get link: %w", err)
	}

	link, apiErr := handleGetLinkRes(res)
	if apiErr != nil {
		return fmt.Errorf("decode get link response: %w", apiErr)
	}

	out := commandWriter(cmd)

	if cmd.Bool(FlagJSON) {
		return errorcontext.Wrap(printJSON(out, link), "print link json")
	}

	return errorcontext.Wrap(printLink(out, link), "print link")
}

func handleGetLinkRes(res padmark.GetLinkRes) (*padmark.LinkResponse, error) {
	link, ok := res.(*padmark.LinkResponse)
	if !ok {
		return nil, responseError(res, linkRecord(), nil)
	}

	// The generated decoder leaves a "#fragment" in the path, and the output would show "%23".
	link.TargetURL = padmark.SplitFragment(link.TargetURL)

	return link, nil
}
