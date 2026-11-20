package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/clifactory"
	padmark "github.com/partyzanex/padmark/pkg/client"
)

// stdoutPath is the --output value that writes the file content to stdout.
const stdoutPath = "-"

// downloadFileMode gives access only to the owner. A downloaded file can be private.
const downloadFileMode = 0o600

func downloadCommand() *urcli.Command {
	return clifactory.Command(func(command *urcli.Command) {
		command.Name = "download"
		command.Usage = "Download a file by ID or URL (a burn-after-download file is deleted after it)"
		command.ArgsUsage = argsUsageFileID
		command.Flags = []urcli.Flag{
			clifactory.StringFlag(func(flag *urcli.StringFlag) {
				flag.Name = FlagOutput
				flag.Aliases = []string{"o"}
				flag.Usage = "Destination path; - writes to stdout (default: the file name from the server, " +
					"in the current directory)"
			}),
			clifactory.BoolFlag(func(flag *urcli.BoolFlag) {
				flag.Name = FlagForce
				flag.Usage = "Replace the destination file if it exists"
			}),
		}
		command.Action = downloadAction
	})
}

func downloadAction(ctx context.Context, cmd *urcli.Command) error {
	id, err := fileIDArg(cmd)
	if err != nil {
		return fmt.Errorf("read file ID: %w", err)
	}

	apiClient, err := newFileTransferClient(cmd)
	if err != nil {
		return fmt.Errorf("create API client: %w", err)
	}

	download := func(dst io.Writer) (int64, error) {
		file, downloadErr := apiClient.DownloadFile(ctx, id, dst)
		if downloadErr != nil {
			return 0, fmt.Errorf("download file: %w", fileTransferError(downloadErr, ErrBurnReadRefused))
		}

		return file.Size, nil
	}

	output := cmd.String(FlagOutput)
	if output == stdoutPath {
		_, err = download(commandWriter(cmd))

		return err
	}

	if output == "" {
		output, err = serverFileName(ctx, apiClient, id)
		if err != nil {
			return fmt.Errorf("resolve destination: %w", err)
		}
	}

	size, err := saveDownload(output, overwriteSetting(cmd.Bool(FlagForce)), download)
	if err != nil {
		return fmt.Errorf("save %s: %w", output, err)
	}

	ew := &errWriter{w: commandWriter(cmd), err: nil}
	ew.printf("saved %s (%d bytes)\n", output, size)

	if ew.err != nil {
		return fmt.Errorf("write output: %w", ew.err)
	}

	return nil
}

// serverFileName reads the file name with GET /files/{id} before the download. That request
// does not consume a burn-after-download file, so an unsafe name or an existing destination
// stops the command before the server deletes the file.
func serverFileName(ctx context.Context, cl *padmark.Client, id string) (string, error) {
	res, err := cl.GetFile(ctx, padmark.GetFileParams{ID: id})
	if err != nil {
		return "", fmt.Errorf("get file: %w", err)
	}

	info, err := handleGetFileRes(res)
	if err != nil {
		return "", fmt.Errorf("decode get file response: %w", err)
	}

	return safeFileName(info.Name)
}

// safeFileName accepts a server file name as a name in the current directory. The uploader
// chooses the name, so a path separator, "." or ".." or a control character is refused.
func safeFileName(name string) (string, error) {
	unsafe := name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\`) || strings.ContainsFunc(name, unicode.IsControl)
	if unsafe {
		return "", fmt.Errorf("%w: %q", errUnsafeFileName, name)
	}

	return name, nil
}

// overwriteSetting is the value of --force. The named type keeps the setting out of a bare bool
// parameter.
type overwriteSetting bool

// keepExisting refuses to replace a destination file that exists.
const keepExisting overwriteSetting = false

// saveDownload writes the download to a temporary file in the destination directory and then
// renames it to path. A failed download thus never leaves a partial file at path. With
// keepExisting, it first makes path with O_EXCL: an existing file stops the command before the
// download, and a file that a different process makes during the download is not replaced.
func saveDownload(path string, overwrite overwriteSetting, download func(io.Writer) (int64, error)) (int64, error) {
	var cleanup []string

	if overwrite == keepExisting {
		err := reservePath(path)
		if err != nil {
			return 0, err
		}

		cleanup = append(cleanup, path)
	}

	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.part")
	if err != nil {
		return 0, errors.Join(fmt.Errorf("create temporary file: %w", err), removeFiles(cleanup))
	}

	cleanup = append(cleanup, temp.Name())

	size, downloadErr := download(temp)

	err = errors.Join(downloadErr, closeFile(temp))
	if err == nil {
		err = renameFile(temp.Name(), path)
	}

	if err != nil {
		return 0, errors.Join(err, removeFiles(cleanup))
	}

	return size, nil
}

// renameFile moves the completed download to its destination.
func renameFile(from, to string) error {
	err := os.Rename(from, to)
	if err != nil {
		return fmt.Errorf("rename temporary file: %w", err)
	}

	return nil
}

// removeFiles removes the files of a failed download. A file that is already gone is not an
// error.
func removeFiles(paths []string) error {
	var errs []error

	for _, path := range paths {
		err := os.Remove(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove %s: %w", path, err))
		}
	}

	return errors.Join(errs...)
}

// reservePath makes an empty file at path. It fails when path exists.
func reservePath(path string) error {
	file, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, downloadFileMode)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%w: %s", errOutputExists, path)
	}

	if err != nil {
		return fmt.Errorf("create destination: %w", err)
	}

	return closeFile(file)
}

// closeFile closes file and adds context to the error.
func closeFile(file *os.File) error {
	err := file.Close()
	if err != nil {
		return fmt.Errorf("close %s: %w", file.Name(), err)
	}

	return nil
}
