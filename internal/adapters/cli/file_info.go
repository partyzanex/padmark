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

func fileInfoCommand() *urcli.Command {
	return clifactory.Command(func(command *urcli.Command) {
		command.Name = "file-info"
		command.Usage = "Get a file's metadata by ID or URL (does not count as a download)"
		command.ArgsUsage = argsUsageFileID
		command.Flags = []urcli.Flag{
			clifactory.BoolFlag(func(flag *urcli.BoolFlag) {
				flag.Name = FlagJSON
				flag.Usage = "Print the metadata as JSON"
			}),
		}
		command.Action = fileInfoAction
	})
}

// fileInfoAction sends only GET /files/{id}. That request does not consume a
// burn-after-download file.
func fileInfoAction(ctx context.Context, cmd *urcli.Command) error {
	id, err := recordIDArg(cmd, fileRecord())
	if err != nil {
		return fmt.Errorf("read file ID: %w", err)
	}

	apiClient, server, err := clientauth.NewClient(clientSettings(cmd))
	if err != nil {
		return fmt.Errorf("create API client: %w", err)
	}

	callCtx, cancel := requestContext(ctx, cmd)
	defer cancel()

	res, err := apiClient.GetFile(callCtx, padmark.GetFileParams{ID: id})
	if err != nil {
		return fmt.Errorf("get file: %w", err)
	}

	info, err := handleGetFileRes(res)
	if err != nil {
		return fmt.Errorf("decode get file response: %w", err)
	}

	return printFileResult(cmd, server.URL, info)
}

// printFileResult prints file metadata as text, or as JSON with --json.
func printFileResult(cmd *urcli.Command, serverURL string, info *padmark.FileInfo) error {
	out := commandWriter(cmd)

	if cmd.Bool(FlagJSON) {
		return errorcontext.Wrap(printJSON(out, info), "print file json")
	}

	return errorcontext.Wrap(printFile(out, serverURL, info), "print file")
}

func handleGetFileRes(res padmark.GetFileRes) (*padmark.FileInfo, error) {
	if typed, ok := res.(*padmark.FileInfo); ok {
		return typed, nil
	}

	return nil, responseError(res, fileRecord(), nil)
}
