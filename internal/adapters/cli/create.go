package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/clientauth"
	"github.com/partyzanex/padmark/internal/clifactory"
	"github.com/partyzanex/padmark/internal/errorcontext"
	padmark "github.com/partyzanex/padmark/pkg/client"
)

const (
	// firstLineScanLimit caps how many lines we inspect when deriving a title from content.
	firstLineScanLimit = 20

	argsUsageID   = "<id>"
	untitledTitle = "Untitled"
)

func createCommand() *urcli.Command {
	return clifactory.Command(func(command *urcli.Command) {
		command.Name = "create"
		command.Usage = "Create a new note"
		command.ArgsUsage = " "
		command.Flags = []urcli.Flag{
			clifactory.StringFlag(func(flag *urcli.StringFlag) {
				flag.Name = FlagTitle
				flag.Aliases = []string{"t"}
				flag.Usage = "Note title (derived from the first non-empty line of content if omitted)"
			}),
			clifactory.StringFlag(func(flag *urcli.StringFlag) {
				flag.Name = FlagContent
				flag.Aliases = []string{"c"}
				flag.Usage = "Note content (reads from stdin if neither --content nor --file is set)"
			}),
			clifactory.StringFlag(func(flag *urcli.StringFlag) {
				flag.Name = FlagFile
				flag.Aliases = []string{"f"}
				flag.Usage = "Read note content from `file`"
			}),
			clifactory.StringFlag(func(flag *urcli.StringFlag) {
				flag.Name = FlagSlug
				flag.Usage = "Custom URL slug (letters, digits, hyphens, underscores)"
			}),
			clifactory.BoolFlag(func(flag *urcli.BoolFlag) {
				flag.Name = FlagPlain
				flag.Usage = "Use text/plain content type instead of text/markdown"
			}),
			clifactory.StringFlag(func(flag *urcli.StringFlag) {
				flag.Name = FlagPrivacy
				flag.Usage = usagePrivacyDefaultPublic
			}),
			clifactory.BoolFlag(func(flag *urcli.BoolFlag) {
				flag.Name = FlagPrivate
				flag.Usage = "Deprecated: same as --privacy authenticated (--private=false: public)"
			}),
			clifactory.BoolFlag(func(flag *urcli.BoolFlag) {
				flag.Name = FlagBurn
				flag.Usage = "Burn after reading: delete on first read (add --ttl for a grace period)"
			}),
			clifactory.BoolFlag(func(flag *urcli.BoolFlag) {
				flag.Name = FlagContents
				flag.Usage = "Show an \"On this page\" sidebar with links to the note headings"
			}),
			clifactory.Int64Flag(func(flag *urcli.Int64Flag) {
				flag.Name = FlagTTL
				flag.Usage = "Lifetime in seconds from creation; with --burn, " +
					"seconds the note survives after the first read"
			}),
			clifactory.StringFlag(func(flag *urcli.StringFlag) {
				flag.Name = FlagEditCode
				flag.Aliases = []string{"e"}
				flag.Usage = "Custom edit code, 8-128 chars (random 12-char code generated if omitted). " +
					psWarnEditCode
				flag.Sources = urcli.EnvVars(EnvEditCode)
			}),
		}
		command.Action = createAction
	})
}

// legacyPrivacy maps the deprecated --private flag to a privacy level. It returns "" when the
// flag is not given.
func legacyPrivacy(cmd *urcli.Command) string {
	if !cmd.IsSet(FlagPrivate) {
		return ""
	}

	if cmd.Bool(FlagPrivate) {
		return string(padmark.CreateNoteRequestPrivacyAuthenticated)
	}

	return string(padmark.CreateNoteRequestPrivacyPublic)
}

// privacyFlag returns the privacy level to send: --privacy, else the --private mapping, else "".
func privacyFlag(cmd *urcli.Command) string {
	if privacy := cmd.String(FlagPrivacy); privacy != "" {
		return privacy
	}

	return legacyPrivacy(cmd)
}

// validatePrivacyFlags rejects --private together with a different --privacy, as the API
// rejects a request whose private and privacy fields disagree.
func validatePrivacyFlags(cmd *urcli.Command) error {
	legacy := legacyPrivacy(cmd)
	privacy := cmd.String(FlagPrivacy)

	if legacy != "" && privacy != "" && legacy != privacy {
		return errPrivateConflict
	}

	return nil
}

// validateTTL rejects an explicit --ttl that ttlFlag refuses. With --burn it is the grace period
// after the first read; without --burn it is a lifetime (from creation for `create`, from the
// update for `edit`), as in the API.
func validateTTL(cmd *urcli.Command) error {
	_, _, err := ttlFlag(cmd)

	return err
}

// ttlFlag is the one --ttl check of all commands. set is false when the flag is not given: an
// unset --ttl means "no expiry" for a new record and "keep the expiry" for an update. An
// explicit value must be positive: silently dropping a zero or negative value would create a
// record without the requested expiry. A value above padmark.MaxTTLSeconds would overflow
// time.Duration: 18446744075 seconds would become a TTL of one second.
func ttlFlag(cmd *urcli.Command) (ttl int64, set bool, err error) {
	if !cmd.IsSet(FlagTTL) {
		return 0, false, nil
	}

	ttl = cmd.Int64(FlagTTL)

	switch {
	case ttl <= 0:
		return 0, false, errNonPositiveTTL
	case ttl > padmark.MaxTTLSeconds:
		return 0, false, errTTLTooLarge
	default:
		return ttl, true, nil
	}
}

func createAction(ctx context.Context, cmd *urcli.Command) error {
	err := validateTTL(cmd)
	if err != nil {
		return fmt.Errorf("validate TTL: %w", err)
	}

	err = validatePrivacyFlags(cmd)
	if err != nil {
		return fmt.Errorf("validate privacy: %w", err)
	}

	content, err := readContent(cmd)
	if err != nil {
		return fmt.Errorf("read note content: %w", err)
	}

	if strings.TrimSpace(content) == "" {
		return ErrEmptyContent
	}

	req := buildCreateReq(cmd, content)

	apiClient, server, err := clientauth.NewClient(clientSettings(cmd))
	if err != nil {
		return fmt.Errorf("create API client: %w", err)
	}

	callCtx, cancel := requestContext(ctx, cmd)
	defer cancel()

	res, err := apiClient.CreateNote(callCtx, req)
	if err != nil {
		return fmt.Errorf("create note: %w", err)
	}

	note, apiErr := handleCreateNoteRes(res)
	if apiErr != nil {
		return fmt.Errorf("decode create note response: %w", apiErr)
	}

	return errorcontext.Wrap(printCreateResult(os.Stdout, server.URL, note), "print create result")
}

func buildCreateReq(cmd *urcli.Command, content string) *padmark.CreateNoteRequest {
	title := cmd.String(FlagTitle)
	if title == "" {
		title = firstLine(content)
	}

	// Optional fields stay unset until the caller supplies them; legacy aliases stay unused.
	req := new(padmark.CreateNoteRequest)
	req.Content = content

	if title != "" {
		req.Title = padmark.NewOptString(title)
	}

	if cmd.Bool(FlagPlain) {
		req.ContentType = padmark.NewOptCreateNoteRequestContentType(
			padmark.CreateNoteRequestContentTypeTextPlain,
		)
	}

	if slug := cmd.String(FlagSlug); slug != "" {
		req.Slug = padmark.NewOptString(slug)
	}

	if p := privacyFlag(cmd); p != "" {
		req.Privacy = padmark.NewOptCreateNoteRequestPrivacy(padmark.CreateNoteRequestPrivacy(p))
	}

	if ec := cmd.String(FlagEditCode); ec != "" {
		req.EditCode = padmark.NewOptString(ec)
	}

	if cmd.Bool(FlagBurn) {
		req.BurnAfterReading = padmark.NewOptBool(true)
	}

	if cmd.Bool(FlagContents) {
		req.ShowContents = padmark.NewOptBool(true)
	}

	if ttl := cmd.Int64(FlagTTL); ttl > 0 {
		req.TTL = padmark.NewOptInt64(ttl)
	}

	return req
}

func handleCreateNoteRes(res padmark.CreateNoteRes) (*padmark.CreateNoteResponse, error) {
	if typed, ok := res.(*padmark.CreateNoteResponseHeaders); ok {
		resp := typed.Response

		return &resp, nil
	}

	return nil, responseError(res, noteRecord(), nil)
}

// firstLine returns the first non-empty, non-heading line of content as a title fallback.
func firstLine(content string) string {
	for _, line := range strings.SplitN(content, "\n", firstLineScanLimit) {
		trimmed := strings.TrimLeft(line, "#")
		if len(trimmed) < len(line) {
			trimmed = strings.TrimPrefix(trimmed, " ")
		}

		line = strings.TrimSpace(trimmed)

		if line != "" {
			return line
		}
	}

	return untitledTitle
}
