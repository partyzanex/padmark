package cli

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/clifactory"
	"github.com/partyzanex/padmark/internal/errorcontext"
	padmark "github.com/partyzanex/padmark/pkg/client"
)

// stdinPath is the path argument that makes `upload` read the file from stdin.
const stdinPath = "-"

func uploadCommand() *urcli.Command {
	return clifactory.Command(func(command *urcli.Command) {
		command.Name = "upload"
		command.Usage = "Upload a file (pass - to read it from stdin)"
		command.ArgsUsage = "<path>"
		command.Flags = []urcli.Flag{
			clifactory.StringFlag(func(flag *urcli.StringFlag) {
				flag.Name = FlagName
				flag.Usage = "File name on the server (default: the base name of <path>; required for stdin)"
			}),
			clifactory.StringFlag(func(flag *urcli.StringFlag) {
				flag.Name = FlagSlug
				flag.Usage = "Custom file ID (letters, digits, hyphens, underscores)"
			}),
			clifactory.StringFlag(func(flag *urcli.StringFlag) {
				flag.Name = FlagPrivacy
				flag.Usage = usagePrivacyDefaultPublic
			}),
			clifactory.BoolFlag(func(flag *urcli.BoolFlag) {
				flag.Name = FlagBurn
				flag.Usage = "Burn after download: delete the file after its first download"
			}),
			clifactory.Int64Flag(func(flag *urcli.Int64Flag) {
				flag.Name = FlagTTL
				flag.Usage = "Seconds until the file expires, counted from the upload (omit for no expiry)"
			}),
			clifactory.StringFlag(func(flag *urcli.StringFlag) {
				flag.Name = FlagEditCode
				flag.Aliases = []string{"e"}
				flag.Usage = "Custom edit code, 8-128 chars (random code generated if omitted). " + psWarnEditCode
				flag.Sources = urcli.EnvVars(EnvEditCode)
			}),
			clifactory.BoolFlag(func(flag *urcli.BoolFlag) {
				flag.Name = FlagJSON
				flag.Usage = "Print the result as JSON"
			}),
		}
		command.Action = uploadAction
	})
}

func uploadAction(ctx context.Context, cmd *urcli.Command) (err error) {
	opts, err := uploadOptions(cmd)
	if err != nil {
		return fmt.Errorf("read upload options: %w", err)
	}

	source, err := openUpload(cmd)
	if err != nil {
		return fmt.Errorf("open upload: %w", err)
	}

	defer func() { err = errors.Join(err, source.close()) }()

	apiClient, err := newFileTransferClient(cmd)
	if err != nil {
		return fmt.Errorf("create API client: %w", err)
	}

	created, err := apiClient.UploadFile(ctx, source.name, source.file, opts)
	if err != nil {
		return fmt.Errorf("upload file: %w", fileTransferError(err, ErrSlugConflict))
	}

	out := commandWriter(cmd)

	if cmd.Bool(FlagJSON) {
		return errorcontext.Wrap(printFileJSON(out, created), "print upload result json")
	}

	return errorcontext.Wrap(printCreateFileResult(out, resolveServerURL(cmd), created), "print upload result")
}

// uploadOptions reads the upload flags. An explicit non-positive --ttl is an error; an unset
// --ttl means no expiry.
func uploadOptions(cmd *urcli.Command) (padmark.UploadFileOptions, error) {
	opts := padmark.UploadFileOptions{
		TTL:               0,
		Privacy:           cmd.String(FlagPrivacy),
		Slug:              cmd.String(FlagSlug),
		EditCode:          cmd.String(FlagEditCode),
		BurnAfterDownload: cmd.Bool(FlagBurn),
	}

	if cmd.IsSet(FlagTTL) {
		ttl, err := fileTTLSeconds(cmd)
		if err != nil {
			return opts, err
		}

		opts.TTL = time.Duration(ttl) * time.Second
	}

	return opts, nil
}

// maxTTLSeconds is the largest --ttl of a file, as on the server. A larger value overflows
// time.Duration: 18446744075 seconds would become a TTL of one second.
const maxTTLSeconds = math.MaxInt64 / int64(time.Second)

// fileTTLSeconds reads --ttl of the file commands. It refuses a value that is not positive or
// that is larger than maxTTLSeconds, before a conversion to time.Duration.
func fileTTLSeconds(cmd *urcli.Command) (int64, error) {
	ttl := cmd.Int64(FlagTTL)

	switch {
	case ttl <= 0:
		return 0, errNonPositiveTTL
	case ttl > maxTTLSeconds:
		return 0, errTTLTooLarge
	default:
		return ttl, nil
	}
}

// uploadSource is the content of an upload and its file name for the server.
type uploadSource struct {
	file *os.File
	name string
	// owned is false for stdin: the command must not close the process stdin.
	owned bool
}

// close closes the source file when the command opened it.
func (src *uploadSource) close() error {
	if !src.owned {
		return nil
	}

	return closeFile(src.file)
}

// openUpload opens the upload source. The caller calls close on the result.
func openUpload(cmd *urcli.Command) (*uploadSource, error) {
	path := cmd.Args().Get(0)
	name := cmd.String(FlagName)

	switch path {
	case "":
		return nil, errFilePathRequired
	case stdinPath:
		return openStdinUpload(name)
	}

	// The user names the file to upload, so any readable path is permitted.
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}

	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("stat file: %w", err), closeFile(file))
	}

	// A directory, device or pipe has no fixed size; the server needs a regular file body.
	if !info.Mode().IsRegular() {
		return nil, errors.Join(fmt.Errorf("%w: %s", errNotRegularFile, path), closeFile(file))
	}

	if name == "" {
		name = filepath.Base(path)
	}

	return &uploadSource{file: file, name: name, owned: true}, nil
}

// openStdinUpload returns stdin as the upload source. stdin has no file name, so name must be
// set. An interactive terminal is an error: the upload would wait for input without a prompt.
func openStdinUpload(name string) (*uploadSource, error) {
	if name == "" {
		return nil, errStdinNameRequired
	}

	stat, err := os.Stdin.Stat()
	if err == nil && isCharDevice(stat.Mode()) {
		return nil, errStdinTerminal
	}

	return &uploadSource{file: os.Stdin, name: name, owned: false}, nil
}
