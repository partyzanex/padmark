package mcp

import (
	"context"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

//go:generate go run go.uber.org/mock/mockgen@latest -source=seam.go -destination=mcp_mocks_test.go -package=mcp

// PadmarkClient is the subset of the generated ogen client (*padmark.Client) the tools need.
// A seam by the project's DIP convention: declared next to the consumer (this package),
// implemented by the concrete generated client, replaced by a generated mock in unit tests.
type PadmarkClient interface {
	CreateNote(ctx context.Context, request *padmark.CreateNoteRequest) (padmark.CreateNoteRes, error)
	GetNote(ctx context.Context, params padmark.GetNoteParams) (padmark.GetNoteRes, error)
	UpdateNote(
		ctx context.Context, request *padmark.UpdateNoteRequest, params padmark.UpdateNoteParams,
	) (padmark.UpdateNoteRes, error)
	DeleteNote(ctx context.Context, params padmark.DeleteNoteParams) (padmark.DeleteNoteRes, error)
	CreateLink(ctx context.Context, request *padmark.CreateLinkRequest) (padmark.CreateLinkRes, error)
	CreateLinksBulk(ctx context.Context, request *padmark.BulkCreateLinksRequest) (padmark.CreateLinksBulkRes, error)
	GetLink(ctx context.Context, params padmark.GetLinkParams) (padmark.GetLinkRes, error)
	DeleteLink(ctx context.Context, params padmark.DeleteLinkParams) (padmark.DeleteLinkRes, error)
	Healthz(ctx context.Context) error
	Readyz(ctx context.Context) (padmark.ReadyzRes, error)
}

// The concrete generated client satisfies the seam structurally — no wrapper is needed.
var _ PadmarkClient = (*padmark.Client)(nil)
