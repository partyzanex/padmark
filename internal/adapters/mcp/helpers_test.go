package mcp

import (
	"context"
	"net/url"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// connectTestClient attaches an in-memory MCP client to the manager's server, so tests drive
// tools exactly the way a real agent does (schema-validated arguments, packed results).
func connectTestClient(ctx context.Context, mgr *Manager) (*mcpsdk.ClientSession, error) {
	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()

	srvSession, err := mgr.srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		return nil, err
	}

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "0"}, nil)

	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		_ = srvSession.Close() // a failed close must not mask the connect error

		return nil, err
	}

	return clientSession, nil
}

// testNoteResponse builds a small successful note fixture for get/update paths.
func testNoteResponse(id string) *padmark.NoteResponse {
	return &padmark.NoteResponse{
		ID: id, Title: "t", Content: "# hi",
		ContentType: padmark.NoteResponseContentTypeTextMarkdown,
	}
}

// testCreateNoteHeaders builds the 201 variant padmark returns with a one-time edit code.
func testCreateNoteHeaders(id, editCode string) *padmark.CreateNoteResponseHeaders {
	return &padmark.CreateNoteResponseHeaders{
		Response: padmark.CreateNoteResponse{
			ID: id, Title: "t", Content: "# hi",
			ContentType: padmark.CreateNoteResponseContentTypeTextMarkdown,
			EditCode:    editCode,
		},
	}
}

// UpdateNote aliases keep the ogen mock stubs' signatures within the line limit; the exact
// generated types must still match, so they are aliases, not new types.
type (
	updReq    = padmark.UpdateNoteRequest
	updParams = padmark.UpdateNoteParams
	updRes    = padmark.UpdateNoteRes
)

type updResultFn = func(context.Context, *updReq, updParams) (updRes, error)

// updateStub wraps the request assertions into an ogen-compat DoAndReturn body.
func updateStub(check func(req *updReq)) updResultFn {
	return func(_ context.Context, req *updReq, _ updParams) (updRes, error) {
		check(req)

		return testNoteResponse("abc123x999"), nil
	}
}

// testURL parses a constant test URL; a failure is a programming error in the fixture itself.
func testURL(t *testing.T, raw string) url.URL {
	t.Helper()

	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse fixture URL %q: %v", raw, err)
	}

	return *parsed
}
