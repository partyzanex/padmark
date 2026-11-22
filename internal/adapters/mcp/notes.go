package mcp

import (
	"context"
	"net/http"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/partyzanex/padmark/internal/errorcontext"
	padmark "github.com/partyzanex/padmark/pkg/client"
)

// registerNoteTools adds the note tools to the server.
func registerNoteTools(srv *mcpsdk.Server, proxy *Server) {
	mcpsdk.AddTool[createNoteInput, noteOutput](srv, createNoteTool(), limitCall(proxy, proxy.createNote))
	mcpsdk.AddTool[getNoteInput, noteOutput](srv, getNoteTool(), limitCall(proxy, proxy.getNote))
	mcpsdk.AddTool[updateNoteInput, noteOutput](srv, updateNoteTool(), limitCall(proxy, proxy.updateNote))
	mcpsdk.AddTool[deleteNoteInput, deletedOutput](srv, deleteNoteTool(), limitCall(proxy, proxy.deleteNote))
}

func createNoteTool() *mcpsdk.Tool {
	return newTool("create_note",
		"Create a Markdown (or plain-text) note on the configured padmark server and return it. The padmark "+
			"edit_code is returned exactly once and is kept in the proxy for update/delete within this session; it "+
			"is shown in the result only when reveal_edit_code is set (this keeps the one-time secret out of "+
			"transcript).",
		additiveTool)
}

func getNoteTool() *mcpsdk.Tool {
	return newTool("get_note",
		"Read a note. Not read-only at padmark: every read bumps the views counter and the first read of a "+
			"burn-after-reading note destroys it.",
		additiveTool)
}

func updateNoteTool() *mcpsdk.Tool {
	return newTool("update_note",
		"Replace a note's content and settings wholesale (like the REST PUT). The edit_code comes from the input, "+
			"else from the proxy's session vault, else from the bearer token (the note's owner may omit the code).",
		idempotentDestructiveTool)
}

func deleteNoteTool() *mcpsdk.Tool {
	return newTool("delete_note",
		"Delete a note permanently. The edit_code comes from the input, else from the proxy's session vault, "+
			"else from the bearer token.",
		destructiveTool)
}

func (s *Server) createNote(
	ctx context.Context, _ *mcpsdk.CallToolRequest,
	input createNoteInput, //nolint:gocritic // hugeParam: the MCP SDK tool signature passes In by value
) (*mcpsdk.CallToolResult, noteOutput, error) {
	// The padmark contract requires non-empty content; say so up front instead of paying a
	// 400 round-trip for something the model can fix immediately.
	if strings.TrimSpace(input.Content) == "" {
		return nil, noteOutput{}, errEmptyContent
	}

	req := newCreateNoteRequest(&input)

	res, err := s.client.CreateNote(ctx, req)

	created, err := fetch[*padmark.CreateNoteResponseHeaders](res, err, "create note", operation{
		context: "decode create note response", rateScope: "note creations", kind: noteRecord(),
	})
	if err != nil {
		return nil, noteOutput{}, err
	}

	out := s.noteOutputFromCreate(&created.Response)
	if input.RevealEditCode && created.Response.EditCode != "" {
		out.revealEditCode(created.Response.EditCode)
	}

	return nil, out, nil
}

// newCreateNoteRequest maps the optional tool input fields to the padmark create request. A field
// that the caller leaves unset is not sent.
func newCreateNoteRequest(input *createNoteInput) *padmark.CreateNoteRequest {
	req := new(padmark.CreateNoteRequest)
	req.Content = input.Content

	if input.Title != nil {
		req.Title = padmark.NewOptString(*input.Title)
	}

	if input.ContentType != nil {
		req.ContentType = padmark.NewOptCreateNoteRequestContentType(
			padmark.CreateNoteRequestContentType(*input.ContentType))
	}

	if input.Slug != nil {
		req.Slug = padmark.NewOptString(*input.Slug)
	}

	if input.BurnAfterReading != nil {
		req.BurnAfterReading = padmark.NewOptBool(*input.BurnAfterReading)
	}

	if input.TTL != nil {
		req.TTL = padmark.NewOptInt64(*input.TTL)
	}

	if input.EditCode != nil {
		req.EditCode = padmark.NewOptString(*input.EditCode)
	}

	if input.Privacy != nil {
		req.Privacy = padmark.NewOptCreateNoteRequestPrivacy(
			padmark.CreateNoteRequestPrivacy(*input.Privacy))
	}

	if input.ShowContents != nil {
		req.ShowContents = padmark.NewOptBool(*input.ShowContents)
	}

	return req
}

func (s *Server) getNote(
	ctx context.Context, _ *mcpsdk.CallToolRequest, input getNoteInput,
) (*mcpsdk.CallToolResult, noteOutput, error) {
	res, err := s.client.GetNote(ctx, padmark.GetNoteParams{ID: input.ID})
	if err != nil {
		return nil, noteOutput{}, errorcontext.Wrap(transportError("get note", err), "handle get note")
	}

	note, err := successOf[*padmark.NoteResponse](res, "map get note res", operation{
		context: "decode get note response", rateScope: "note reads", kind: noteRecord(),
	}, statusRules{
		http.StatusConflict: burnReadRefused,
	})
	if err != nil {
		return nil, noteOutput{}, err
	}

	return nil, noteOutputFromNote(note), nil
}

func (s *Server) updateNote(
	ctx context.Context, _ *mcpsdk.CallToolRequest,
	input updateNoteInput, //nolint:gocritic // hugeParam: the MCP SDK tool signature passes In by value
) (*mcpsdk.CallToolResult, noteOutput, error) {
	editCode := editCodeFor(s.vault, input.ID, input.EditCode)

	req := new(padmark.UpdateNoteRequest)
	req.Content = input.Content

	if input.Title != nil {
		req.Title = padmark.NewOptString(*input.Title)
	}

	if input.ContentType != nil {
		req.ContentType = padmark.NewOptUpdateNoteRequestContentType(
			padmark.UpdateNoteRequestContentType(*input.ContentType))
	}

	if input.BurnAfterReading != nil {
		req.BurnAfterReading = padmark.NewOptBool(*input.BurnAfterReading)
	}

	if input.TTL != nil {
		req.TTL = padmark.NewOptNilInt64(*input.TTL)
	}

	if input.Privacy != nil {
		req.Privacy = padmark.NewOptUpdateNoteRequestPrivacy(
			padmark.UpdateNoteRequestPrivacy(*input.Privacy))
	}

	if input.ShowContents != nil {
		req.ShowContents = padmark.NewOptBool(*input.ShowContents)
	}

	if editCode != "" {
		req.EditCode = padmark.NewOptString(editCode)
	}

	res, err := s.client.UpdateNote(ctx, req, padmark.UpdateNoteParams{ID: input.ID})

	note, err := fetch[*padmark.NoteResponse](res, err, "update note", operation{
		context: "decode update note response", rateScope: "note updates", kind: noteRecord(),
	})
	if err != nil {
		return nil, noteOutput{}, err
	}

	return nil, noteOutputFromNote(note), nil
}

func (s *Server) deleteNote(
	ctx context.Context, _ *mcpsdk.CallToolRequest, input deleteNoteInput,
) (*mcpsdk.CallToolResult, deletedOutput, error) {
	var params padmark.DeleteNoteParams

	params.ID = input.ID

	if code := editCodeFor(s.vault, input.ID, input.EditCode); code != "" {
		// The header, not the query parameter — query strings land in access logs.
		params.XEditCode = padmark.NewOptString(code)
	}

	res, err := s.client.DeleteNote(ctx, params)

	_, err = fetch[*padmark.DeleteNoteNoContent](res, err, "delete note", operation{
		context: "delete note response", rateScope: "note deletes", kind: noteRecord(),
	})
	if err != nil {
		return nil, deletedOutput{}, err
	}

	return nil, deletedOutput{ID: input.ID, Deleted: true}, nil
}
