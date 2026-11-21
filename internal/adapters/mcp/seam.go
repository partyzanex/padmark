package mcp

import (
	"context"
	"io"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

//go:generate go run go.uber.org/mock/mockgen@latest -source=seam.go -destination=mcp_mocks_test.go -package=mcp -write_package_comment=false

// PadmarkClient is the subset of the generated ogen client (*padmark.Client) the tools need.
// A seam by the project's DIP convention: declared next to the consumer (this package),
// implemented by the concrete generated client, replaced by a generated mock in unit tests.
// One server has one client, so the Manager takes this one seam for all tools.
// The seam is a union of the tool groups below. Each group stays under the interfacebloat
// limit of 10 methods; the Manager still gets one client, not one client for each group.
// Errors are fail-hard for each call: the tool returns the error to the agent as its result.
type PadmarkClient interface {
	NoteAPI
	LinkAPI
	FileAPI
	ProbeAPI
}

// NoteAPI is the part of PadmarkClient that the note tools use.
type NoteAPI interface {
	CreateNote(ctx context.Context, request *padmark.CreateNoteRequest) (padmark.CreateNoteRes, error)
	GetNote(ctx context.Context, params padmark.GetNoteParams) (padmark.GetNoteRes, error)
	UpdateNote(
		ctx context.Context, request *padmark.UpdateNoteRequest, params padmark.UpdateNoteParams,
	) (padmark.UpdateNoteRes, error)
	DeleteNote(ctx context.Context, params padmark.DeleteNoteParams) (padmark.DeleteNoteRes, error)
}

// LinkAPI is the part of PadmarkClient that the link tools use.
type LinkAPI interface {
	CreateLink(ctx context.Context, request *padmark.CreateLinkRequest) (padmark.CreateLinkRes, error)
	CreateLinksBulk(ctx context.Context, request *padmark.BulkCreateLinksRequest) (padmark.CreateLinksBulkRes, error)
	GetLink(ctx context.Context, params padmark.GetLinkParams) (padmark.GetLinkRes, error)
	DeleteLink(ctx context.Context, params padmark.DeleteLinkParams) (padmark.DeleteLinkRes, error)
}

// FileAPI is the part of PadmarkClient that the file tools use.
type FileAPI interface {
	// UploadFile is the hand-written streaming upload of pkg/client. The generated CreateFile
	// sends the file part before the option fields, so the server would ignore the options.
	UploadFile(
		ctx context.Context, name string, body io.Reader, opts padmark.UploadFileOptions,
	) (*padmark.CreateFileResponse, error)
	GetFile(ctx context.Context, params padmark.GetFileParams) (padmark.GetFileRes, error)
	UpdateFile(
		ctx context.Context, request *padmark.UpdateFileRequest, params padmark.UpdateFileParams,
	) (padmark.UpdateFileRes, error)
	DeleteFile(ctx context.Context, params padmark.DeleteFileParams) (padmark.DeleteFileRes, error)
}

// ProbeAPI is the part of PadmarkClient that check_server uses.
type ProbeAPI interface {
	Healthz(ctx context.Context) error
	Readyz(ctx context.Context) (padmark.ReadyzRes, error)
}
