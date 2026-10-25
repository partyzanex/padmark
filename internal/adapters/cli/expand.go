package cli

import (
	"context"
	"fmt"
	"os"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/errorcontext"
	padmark "github.com/partyzanex/padmark/pkg/client"
)

func expandCommand() *urcli.Command {
	return &urcli.Command{
		Name:      "expand",
		Usage:     "Get a short link's metadata by ID",
		ArgsUsage: argsUsageID,
		Flags: []urcli.Flag{
			&urcli.BoolFlag{
				Name:  FlagJSON,
				Usage: "Print the link as JSON",
			},
		},
		Action: expandAction,
	}
}

func expandAction(ctx context.Context, cmd *urcli.Command) error {
	id, err := linkIDArg(cmd)
	if err != nil {
		return fmt.Errorf("read link ID: %w", err)
	}

	cl, err := newPadmarkClient(cmd)
	if err != nil {
		return fmt.Errorf("create API client: %w", err)
	}

	res, err := cl.GetLink(ctx, padmark.GetLinkParams{ID: id})
	if err != nil {
		return fmt.Errorf("get link: %w", err)
	}

	link, apiErr := handleGetLinkRes(res)
	if apiErr != nil {
		return fmt.Errorf("decode get link response: %w", apiErr)
	}

	if cmd.Bool(FlagJSON) {
		return errorcontext.Wrap(printLinkJSON(os.Stdout, link), "print link json")
	}

	return errorcontext.Wrap(printLink(os.Stdout, link), "print link")
}

func handleGetLinkRes(res padmark.GetLinkRes) (*padmark.LinkResponse, error) {
	switch typed := res.(type) {
	case *padmark.LinkResponse:
		return typed, nil
	case *padmark.GetLinkUnauthorized:
		return nil, fmt.Errorf("%w: %s", ErrUnauthorized, typed.Message)
	case *padmark.GetLinkNotFound:
		return nil, ErrLinkNotFound
	case *padmark.GetLinkGone:
		return nil, ErrLinkGone
	case *padmark.GetLinkServiceUnavailable:
		return nil, errorcontext.Wrap(serviceUnavailableError(padmark.ErrorResponse(*typed)), "decode get link response")
	case *padmark.GetLinkInternalServerError:
		return nil, fmt.Errorf("%w: %s", ErrServer, typed.Message)
	default:
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResponse, res)
	}
}
