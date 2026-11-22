package cli

import (
	"context"
	"fmt"

	urcli "github.com/urfave/cli/v3"

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
	id, err := linkIDArg(cmd)
	if err != nil {
		return fmt.Errorf("read link ID: %w", err)
	}

	apiClient, err := newPadmarkClient(cmd)
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
		return errorcontext.Wrap(printLinkJSON(out, link), "print link json")
	}

	return errorcontext.Wrap(printLink(out, link), "print link")
}

func handleGetLinkRes(res padmark.GetLinkRes) (*padmark.LinkResponse, error) {
	switch typed := res.(type) {
	case *padmark.LinkResponse:
		// The generated decoder leaves a "#fragment" in the path, and the output would show "%23".
		typed.TargetURL = padmark.SplitFragment(typed.TargetURL)

		return typed, nil
	case *padmark.GetLinkUnauthorized:
		return nil, fmt.Errorf("%w: %s", ErrUnauthorized, typed.Message)
	case *padmark.GetLinkNotFound:
		return nil, ErrLinkNotFound
	case *padmark.GetLinkGone:
		return nil, ErrLinkGone
	case *padmark.GetLinkServiceUnavailable:
		return nil, errorcontext.Wrap(
			serviceUnavailableError(padmark.ErrorResponse(*typed)), "decode get link response")
	case *padmark.GetLinkInternalServerError:
		return nil, fmt.Errorf("%w: %s", ErrServer, typed.Message)
	default:
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResponse, res)
	}
}
