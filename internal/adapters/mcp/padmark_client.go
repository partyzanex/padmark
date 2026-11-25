package mcp

import (
	"context"
	"io"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// The mocks cover only PadmarkClient: the tests replace the whole client, never one group.
//go:generate go run go.uber.org/mock/mockgen@latest -source=padmark_client.go -destination=padmark_client_mocks_test.go -package=mcp -write_package_comment=false -exclude_interfaces=noteAPI,linkAPI,fileAPI,probeAPI

// PadmarkClient is the subset of the generated ogen client (*padmark.Client) the tools need.
// A seam by the project's DIP convention: declared next to the consumer (this package),
// implemented by the concrete generated client, replaced by a generated mock in unit tests.
// One server has one client, so the Server takes this one seam for all tools.
// The seam is a union of the unexported tool groups below only because each group stays under
// the interfacebloat limit of 10 methods. The groups are not separate dependencies.
// Errors are fail-hard for each call: the tool returns the error to the agent as its result.
type PadmarkClient interface {
	noteAPI
	linkAPI
	fileAPI
	probeAPI
}

// noteAPI is the part of PadmarkClient that the note tools use.
type noteAPI interface {
	CreateNote(ctx context.Context, request *padmark.CreateNoteRequest) (padmark.CreateNoteRes, error)
	GetNote(ctx context.Context, params padmark.GetNoteParams) (padmark.GetNoteRes, error)
	UpdateNote(
		ctx context.Context, request *padmark.UpdateNoteRequest, params padmark.UpdateNoteParams,
	) (padmark.UpdateNoteRes, error)
	DeleteNote(ctx context.Context, params padmark.DeleteNoteParams) (padmark.DeleteNoteRes, error)
}

// linkAPI is the part of PadmarkClient that the link tools use.
type linkAPI interface {
	CreateLink(ctx context.Context, request *padmark.CreateLinkRequest) (padmark.CreateLinkRes, error)
	CreateLinksBulk(ctx context.Context, request *padmark.BulkCreateLinksRequest) (padmark.CreateLinksBulkRes, error)
	GetLink(ctx context.Context, params padmark.GetLinkParams) (padmark.GetLinkRes, error)
	UpdateLink(
		ctx context.Context, request *padmark.UpdateLinkRequest, params padmark.UpdateLinkParams,
	) (padmark.UpdateLinkRes, error)
	DeleteLink(ctx context.Context, params padmark.DeleteLinkParams) (padmark.DeleteLinkRes, error)
}

// fileAPI is the part of PadmarkClient that the file tools use.
type fileAPI interface {
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

// probeAPI is the part of PadmarkClient that check_server uses.
type probeAPI interface {
	Healthz(ctx context.Context) error
	Readyz(ctx context.Context) (padmark.ReadyzRes, error)
}
