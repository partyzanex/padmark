package cli

import (
	"context"
	"fmt"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/clifactory"
	padmark "github.com/partyzanex/padmark/pkg/client"
)

func updateFileCommand() *urcli.Command {
	return clifactory.Command(func(command *urcli.Command) {
		command.Name = "update-file"
		command.Usage = "Change a file's expiry, privacy or burn rule; an omitted flag keeps the value"
		command.ArgsUsage = argsUsageFileID
		command.Flags = []urcli.Flag{
			clifactory.StringFlag(func(flag *urcli.StringFlag) {
				flag.Name = FlagEditCode
				flag.Aliases = []string{"e"}
				flag.Sources = urcli.EnvVars(EnvEditCode)
				flag.Usage = "Edit code returned at upload (env: PADMARK_EDIT_CODE). Optional if " +
					"--token identifies the file's owner. " + psWarnEditCode
			}),
			clifactory.Int64Flag(func(flag *urcli.Int64Flag) {
				flag.Name = FlagTTL
				flag.Usage = "Seconds until the file expires, counted from this change"
			}),
			clifactory.BoolFlag(func(flag *urcli.BoolFlag) {
				flag.Name = FlagClearExpiry
				flag.Usage = "Remove the expiry: the file does not expire"
			}),
			clifactory.StringFlag(func(flag *urcli.StringFlag) {
				flag.Name = FlagPrivacy
				flag.Usage = "Privacy level: public, authenticated, or owner"
			}),
			clifactory.BoolFlag(func(flag *urcli.BoolFlag) {
				flag.Name = FlagBurn
				flag.Usage = "Burn after download (--burn=false turns it off; omit to keep)"
			}),
			clifactory.BoolFlag(func(flag *urcli.BoolFlag) {
				flag.Name = FlagJSON
				flag.Usage = "Print the result as JSON"
			}),
		}
		command.Action = updateFileAction
	})
}

func updateFileAction(ctx context.Context, cmd *urcli.Command) error {
	id, err := fileIDArg(cmd)
	if err != nil {
		return fmt.Errorf("read file ID: %w", err)
	}

	req, err := buildUpdateFileReq(cmd)
	if err != nil {
		return fmt.Errorf("build update file request: %w", err)
	}

	apiClient, err := newPadmarkClient(cmd)
	if err != nil {
		return fmt.Errorf("create API client: %w", err)
	}

	params := padmark.UpdateFileParams{ID: id, XEditCode: optEditCode(cmd.String(FlagEditCode))}

	res, err := apiClient.UpdateFile(ctx, req, params)
	if err != nil {
		return fmt.Errorf("update file: %w", err)
	}

	info, err := handleUpdateFileRes(res)
	if err != nil {
		return fmt.Errorf("decode update file response: %w", err)
	}

	return printFileResult(cmd, info)
}

// buildUpdateFileReq sends only the fields of the flags that the user set. The server keeps
// the value of each omitted field, so --burn uses cmd.IsSet to tell "omitted" from
// --burn=false.
func buildUpdateFileReq(cmd *urcli.Command) (*padmark.UpdateFileRequest, error) {
	clearExpiry := cmd.Bool(FlagClearExpiry)

	if cmd.IsSet(FlagTTL) && clearExpiry {
		return nil, errTTLConflict
	}

	req := &padmark.UpdateFileRequest{
		TTL:               padmark.OptNilInt64{Value: 0, Set: false, Null: false},
		Privacy:           padmark.OptUpdateFileRequestPrivacy{Value: "", Set: false},
		BurnAfterDownload: padmark.OptBool{Value: false, Set: false},
	}

	if cmd.IsSet(FlagTTL) {
		// The API reads 0 as "expire now"; the CLI refuses it as for the other commands.
		ttl, err := fileTTLSeconds(cmd)
		if err != nil {
			return nil, err
		}

		req.TTL = padmark.NewOptNilInt64(ttl)
	}

	if clearExpiry {
		req.TTL = padmark.OptNilInt64{Value: 0, Set: true, Null: true}
	}

	if privacy := cmd.String(FlagPrivacy); privacy != "" {
		req.Privacy = padmark.NewOptUpdateFileRequestPrivacy(padmark.UpdateFileRequestPrivacy(privacy))
	}

	if cmd.IsSet(FlagBurn) {
		req.BurnAfterDownload = padmark.NewOptBool(cmd.Bool(FlagBurn))
	}

	if !req.TTL.Set && !req.Privacy.Set && !req.BurnAfterDownload.Set {
		return nil, errNoFileChange
	}

	return req, nil
}

func handleUpdateFileRes(res padmark.UpdateFileRes) (*padmark.FileInfo, error) {
	if typed, ok := res.(*padmark.FileInfo); ok {
		return typed, nil
	}

	err := updateFileClientError(res)
	if err != nil {
		return nil, err
	}

	switch typed := res.(type) {
	case *padmark.UpdateFileServiceUnavailable:
		return nil, fileServiceUnavailableError(typed.Code.Or(""), typed.Message)
	case *padmark.UpdateFileInternalServerError:
		return nil, fmt.Errorf("%w: %s", ErrServer, typed.Message)
	default:
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResponse, res)
	}
}

// updateFileClientError maps the client-error variants of an UpdateFile response. It returns nil
// for every other variant, so the caller handles those.
func updateFileClientError(res padmark.UpdateFileRes) error {
	switch typed := res.(type) {
	case *padmark.UpdateFileBadRequest:
		return fmt.Errorf("%w: %s", ErrBadRequest, typed.Message)
	case *padmark.UpdateFileUnauthorized:
		return ErrUnauthorized
	case *padmark.UpdateFileForbidden:
		return errFileOwnershipRequired
	case *padmark.UpdateFileNotFound:
		return ErrFileNotFound
	case *padmark.UpdateFileUnprocessableEntity:
		return fmt.Errorf("%w: %s", ErrValidation, typed.Message)
	case *padmark.UpdateFileRequestEntityTooLarge:
		return fmt.Errorf("%w: %s", ErrRequestTooLarge, typed.Message)
	case *padmark.UpdateFileTooManyRequests:
		return ErrRateLimited
	default:
		return nil
	}
}
