package cli

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// TestResponseTexts pins the error text of each status whose CLI error differs between commands.
// The texts are the texts of the per-command switches that the shared table replaced.
func TestResponseTexts(t *testing.T) {
	t.Parallel()

	// noCode is the absent machine-readable code of an error body.
	noCode := padmark.OptString{Value: "", Set: false}
	handled := func(_ any, err error) error { return err }

	cases := map[string]struct {
		err  error
		want string
	}{
		"get note 401": {
			err:  handled(handleGetNoteRes(&padmark.GetNoteUnauthorized{Message: "m", Code: noCode})),
			want: "unauthorized: authenticate with a credential allowed to read this note",
		},
		"get note 409": {
			err:  handled(handleGetNoteRes(&padmark.GetNoteConflict{Message: "m", Code: noCode})),
			want: "refused: m — the note is still intact",
		},
		"get note 503 api disabled": {
			err: handled(handleGetNoteRes(&padmark.GetNoteServiceUnavailable{
				Message: "m", Code: padmark.NewOptString("api_disabled"),
			})),
			want: ErrAPIDisabled.Error(),
		},
		"update note 401": {
			err: handled(handleUpdateNoteRes(&padmark.UpdateNoteUnauthorized{})), want: "unauthorized",
		},
		"update note 403": {
			err:  handled(handleUpdateNoteRes(&padmark.UpdateNoteForbidden{Message: "m", Code: noCode})),
			want: "forbidden: wrong edit code",
		},
		"update note 413": {
			err:  handled(handleUpdateNoteRes(&padmark.UpdateNoteRequestEntityTooLarge{Message: "m", Code: noCode})),
			want: "content too large: m",
		},
		"update note 415": {
			err:  handled(handleUpdateNoteRes(&padmark.UpdateNoteUnsupportedMediaType{Message: "m", Code: noCode})),
			want: "bad request: m",
		},
		"create note 401": {
			err:  handled(handleCreateNoteRes(&padmark.CreateNoteUnauthorized{Message: "m", Code: noCode})),
			want: "unauthorized: m",
		},
		"create link 413": {
			err:  handled(handleCreateLinkRes(&padmark.CreateLinkRequestEntityTooLarge{Message: "m", Code: noCode})),
			want: "request too large: m",
		},
		"delete note 403": {
			err:  handleDeleteNoteRes(&padmark.DeleteNoteForbidden{Message: "m", Code: noCode}),
			want: "forbidden: wrong edit code or --token is not the note's owner",
		},
		"delete file 401": {
			err: handleDeleteFileRes(&padmark.DeleteFileUnauthorized{Message: "m", Code: noCode}), want: "unauthorized",
		},
		"delete file 503 files off": {
			err:  handleDeleteFileRes(&padmark.DeleteFileServiceUnavailable{Message: filesOffMessage, Code: noCode}),
			want: "service unavailable: file publishing is off on the server",
		},
		"get file 500": {
			err:  handled(handleGetFileRes(&padmark.GetFileInternalServerError{Message: "m", Code: noCode})),
			want: "server error: m",
		},
		"create token 429": {
			err:  handled(handleCreateAPITokenRes(&padmark.CreateAPITokenTooManyRequests{Message: "m", Code: noCode})),
			want: "token cap reached (revoke one via /admin): m",
		},
		"create invite 403": {
			err:  handled(handleCreateInviteRes(&padmark.CreateInviteForbidden{Message: "m", Code: noCode})),
			want: "forbidden (admin and a valid step-up required): m",
		},
		"create invite 429": {
			err:  handled(handleCreateInviteRes(&padmark.CreateInviteTooManyRequests{Message: "m", Code: noCode})),
			want: "rate limit exceeded, try again later",
		},
		"upload 403": {
			err: fileTransferError(&padmark.APIError{
				Message: "m", Code: "", StatusCode: http.StatusForbidden,
			}, ErrSlugConflict),
			want: "forbidden: m",
		},
		"upload 409": {
			err: fileTransferError(&padmark.APIError{
				Message: "m", Code: "", StatusCode: http.StatusConflict,
			}, ErrSlugConflict),
			want: "slug conflict: m",
		},
		"upload 413": {
			err: fileTransferError(&padmark.APIError{
				Message: "m", Code: "", StatusCode: http.StatusRequestEntityTooLarge,
			}, ErrSlugConflict),
			want: "file is larger than the server limit: m",
		},
		"download 502": {
			err:  fileTransferError(&padmark.APIError{Message: "m", Code: "", StatusCode: http.StatusBadGateway}, nil),
			want: "server error: m",
		},
		"download 418": {
			err:  fileTransferError(&padmark.APIError{Message: "m", Code: "", StatusCode: http.StatusTeapot}, nil),
			want: "unexpected response type: status 418: m",
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.EqualError(t, testCase.err, testCase.want)
		})
	}
}
