package cli

import (
	"context"
	"fmt"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/errorcontext"
	padmark "github.com/partyzanex/padmark/pkg/client"
)

func deleteFileCommand() *urcli.Command {
	return newDeleteCommand("delete-file", "Delete a file by ID or URL", "file", deleteFileAction)
}

// deleteFileAction does not require --edit-code: the server also accepts the request when
// --token identifies the file's owner. The edit code goes only in the X-Edit-Code header.
func deleteFileAction(ctx context.Context, cmd *urcli.Command) error {
	deleteFile := func(ctx context.Context, padmarkClient *padmark.Client, id, editCode string) error {
		res, err := padmarkClient.DeleteFile(ctx, padmark.DeleteFileParams{ID: id, XEditCode: optEditCode(editCode)})
		if err != nil {
			return fmt.Errorf("delete file: %w", err)
		}

		return errorcontext.Wrap(handleDeleteFileRes(res), "decode delete file response")
	}

	return errorcontext.Wrap(runDeleteCommand(ctx, cmd, fileIDArg, deleteFile), "delete resource")
}

func handleDeleteFileRes(res padmark.DeleteFileRes) error {
	switch typed := res.(type) {
	case *padmark.DeleteFileNoContent:
		return nil
	case *padmark.DeleteFileUnauthorized:
		return ErrUnauthorized
	case *padmark.DeleteFileForbidden:
		return errFileOwnershipRequired
	case *padmark.DeleteFileNotFound:
		return ErrFileNotFound
	case *padmark.DeleteFileTooManyRequests:
		return ErrRateLimited
	case *padmark.DeleteFileServiceUnavailable:
		return fileServiceUnavailableError(typed.Code.Or(""), typed.Message)
	case *padmark.DeleteFileInternalServerError:
		return fmt.Errorf("%w: %s", ErrServer, typed.Message)
	default:
		return fmt.Errorf("%w: %T", ErrUnexpectedResponse, res)
	}
}
