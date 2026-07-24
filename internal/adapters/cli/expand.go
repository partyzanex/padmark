package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	urcli "github.com/urfave/cli/v3"

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
		return err
	}

	cl, err := newPadmarkClient(cmd)
	if err != nil {
		return err
	}

	res, err := cl.GetLink(ctx, padmark.GetLinkParams{ID: id})
	if err != nil {
		return fmt.Errorf("get link: %w", err)
	}

	link, apiErr := handleGetLinkRes(res)
	if apiErr != nil {
		return apiErr
	}

	if cmd.Bool(FlagJSON) {
		return printLinkJSON(os.Stdout, link)
	}

	return printLink(os.Stdout, link)
}

func handleGetLinkRes(res padmark.GetLinkRes) (*padmark.LinkResponse, error) {
	switch typed := res.(type) {
	case *padmark.LinkResponse:
		return typed, nil
	case *padmark.GetLinkUnauthorized:
		return nil, fmt.Errorf("unauthorized: %s", typed.Message)
	case *padmark.GetLinkNotFound:
		return nil, errors.New("link not found")
	case *padmark.GetLinkGone:
		return nil, errors.New("link has expired")
	case *padmark.GetLinkInternalServerError:
		return nil, fmt.Errorf("server error: %s", typed.Message)
	default:
		return nil, fmt.Errorf("unexpected response type: %T", res)
	}
}
