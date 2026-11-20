package cli

import (
	"context"
	"fmt"

	urcli "github.com/urfave/cli/v3"

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
	id, err := fileIDArg(cmd)
	if err != nil {
		return fmt.Errorf("read file ID: %w", err)
	}

	cl, err := newPadmarkClient(cmd)
	if err != nil {
		return fmt.Errorf("create API client: %w", err)
	}

	res, err := cl.GetFile(ctx, padmark.GetFileParams{ID: id})
	if err != nil {
		return fmt.Errorf("get file: %w", err)
	}

	info, err := handleGetFileRes(res)
	if err != nil {
		return fmt.Errorf("decode get file response: %w", err)
	}

	return printFileResult(cmd, info)
}

// printFileResult prints file metadata as text, or as JSON with --json.
func printFileResult(cmd *urcli.Command, info *padmark.FileInfo) error {
	out := commandWriter(cmd)

	if cmd.Bool(FlagJSON) {
		return errorcontext.Wrap(printFileJSON(out, info), "print file json")
	}

	return errorcontext.Wrap(printFile(out, resolveServerURL(cmd), info), "print file")
}

func handleGetFileRes(res padmark.GetFileRes) (*padmark.FileInfo, error) {
	switch typed := res.(type) {
	case *padmark.FileInfo:
		return typed, nil
	case *padmark.GetFileUnauthorized:
		return nil, fmt.Errorf("%w: %s", ErrUnauthorized, typed.Message)
	case *padmark.GetFileNotFound:
		return nil, ErrFileNotFound
	case *padmark.GetFileGone:
		return nil, ErrFileGone
	case *padmark.GetFileServiceUnavailable:
		return nil, fileServiceUnavailableError(typed.Code.Or(""), typed.Message)
	case *padmark.GetFileInternalServerError:
		return nil, fmt.Errorf("%w: %s", ErrServer, typed.Message)
	default:
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResponse, res)
	}
}
