package mcp

import (
	"context"
	"errors"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// registerNoteTools adds the note tools to the server.
func registerNoteTools(srv *mcpsdk.Server, m *Manager) {
	mcpsdk.AddTool[createNoteInput, createNoteOutput](srv, createNoteTool(), m.createNote)
	mcpsdk.AddTool[getNoteInput, createNoteOutput](srv, getNoteTool(), m.getNote)
	mcpsdk.AddTool[updateNoteInput, createNoteOutput](srv, updateNoteTool(), m.updateNote)
	mcpsdk.AddTool[deleteNoteInput, deleteNoteOutput](srv, deleteNoteTool(), m.deleteNote)
}

func createNoteTool() *mcpsdk.Tool {
	return newTool("create_note",
		"Create a Markdown (or plain-text) note on the configured padmark server and return it. The padmark "+
			"edit_code is returned exactly once and is kept in the proxy for update/delete within this session; it "+
			"is shown in the result only when reveal_edit_code is set (this keeps the one-time secret out of transcript).",
		hints(false, false, false))
}

func getNoteTool() *mcpsdk.Tool {
	return newTool("get_note",
		"Read a note. Not read-only at padmark: every read bumps the views counter and the first read of a "+
			"burn-after-reading note destroys it.",
		hints(false, false, false))
}

func updateNoteTool() *mcpsdk.Tool {
	return newTool("update_note",
		"Replace a note's content and settings wholesale (like the REST PUT). The edit_code comes from the input, "+
			"else from the proxy's session vault, else from the bearer token (the note's owner may omit the code).",
		hints(false, true, true))
}

func deleteNoteTool() *mcpsdk.Tool {
	return newTool("delete_note",
		"Delete a note permanently. The edit_code comes from the input, else from the proxy's session vault, "+
			"else from the bearer token.",
		hints(false, true, false))
}

func (m *Manager) createNote(
	ctx context.Context, _ *mcpsdk.CallToolRequest, input createNoteInput, //nolint:gocritic,lll // In arrives by value; the SDK tool signature fixes this line
) (*mcpsdk.CallToolResult, createNoteOutput, error) {
	// The padmark contract requires non-empty content; say so up front instead of paying a
	// 400 round-trip for something the model can fix immediately.
	if strings.TrimSpace(input.Content) == "" {
		return nil, createNoteOutput{}, errors.New("content must not be empty")
	}

	req := &padmark.CreateNoteRequest{
		Content:          input.Content,
		Title:            padmark.OptString{Value: "", Set: false},
		ContentType:      padmark.OptCreateNoteRequestContentType{Value: "", Set: false},
		Slug:             padmark.OptString{Value: "", Set: false},
		BurnAfterReading: padmark.OptBool{Value: false, Set: false},
		TTL:              padmark.OptInt64{Value: 0, Set: false},
		EditCode:         padmark.OptString{Value: "", Set: false},
		Privacy:          padmark.OptCreateNoteRequestPrivacy{Value: "", Set: false},
		Private:          padmark.OptBool{Value: false, Set: false}, //nolint:staticcheck // deprecated alias, unused
	}
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

	res, err := m.client.CreateNote(ctx, req)
	if err != nil {
		return nil, createNoteOutput{}, transportError("create note", err)
	}

	resp, apiErr := mapCreateNoteRes(res)
	if apiErr != nil {
		return nil, createNoteOutput{}, apiErr
	}

	return nil, m.noteOutputFromCreate(resp, input.RevealEditCode), nil
}

func (m *Manager) getNote(
	ctx context.Context, _ *mcpsdk.CallToolRequest, input getNoteInput,
) (*mcpsdk.CallToolResult, createNoteOutput, error) {
	res, err := m.client.GetNote(ctx, padmark.GetNoteParams{ID: input.ID})
	if err != nil {
		return nil, createNoteOutput{}, transportError("get note", err)
	}

	resp, apiErr := mapGetNoteRes(res)
	if apiErr != nil {
		return nil, createNoteOutput{}, apiErr
	}

	return nil, noteOutputFromNote(resp), nil
}

func (m *Manager) updateNote(
	ctx context.Context, _ *mcpsdk.CallToolRequest, input updateNoteInput, //nolint:gocritic,lll // In arrives by value; the SDK tool signature fixes this line
) (*mcpsdk.CallToolResult, createNoteOutput, error) {
	editCode := editCodeFor(m.vault, input.ID, input.EditCode)

	req := &padmark.UpdateNoteRequest{
		Content:          input.Content,
		Title:            padmark.OptString{Value: "", Set: false},
		ContentType:      padmark.OptUpdateNoteRequestContentType{Value: "", Set: false},
		EditCode:         padmark.OptString{Value: "", Set: false},
		BurnAfterReading: padmark.OptBool{Value: false, Set: false},
		TTL:              padmark.OptNilInt64{Value: 0, Set: false, Null: false},
		Privacy:          padmark.OptUpdateNoteRequestPrivacy{Value: "", Set: false},
		Private:          padmark.OptBool{Value: false, Set: false}, //nolint:staticcheck // deprecated alias, unused
	}
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

	if editCode != "" {
		req.EditCode = padmark.NewOptString(editCode)
	}

	res, err := m.client.UpdateNote(ctx, req, padmark.UpdateNoteParams{ID: input.ID})
	if err != nil {
		return nil, createNoteOutput{}, transportError("update note", err)
	}

	resp, apiErr := mapUpdateNoteRes(res)
	if apiErr != nil {
		return nil, createNoteOutput{}, apiErr
	}

	return nil, noteOutputFromNote(resp), nil
}

//nolint:dupl // the delete-link tool repeats this skeleton over a distinct generated ogen type
func (m *Manager) deleteNote(
	ctx context.Context, _ *mcpsdk.CallToolRequest, input deleteNoteInput,
) (*mcpsdk.CallToolResult, deleteNoteOutput, error) {
	params := padmark.DeleteNoteParams{
		ID:        input.ID,
		XEditCode: padmark.OptString{Value: "", Set: false},
	}
	if code := editCodeFor(m.vault, input.ID, input.EditCode); code != "" {
		// The header, not the query parameter — query strings land in access logs.
		params.XEditCode = padmark.NewOptString(code)
	}

	res, err := m.client.DeleteNote(ctx, params)
	if err != nil {
		return nil, deleteNoteOutput{}, transportError("delete note", err)
	}

	apiErr := mapDeleteNoteRes(res)
	if apiErr != nil {
		return nil, deleteNoteOutput{}, apiErr
	}

	return nil, deleteNoteOutput{ID: input.ID, Deleted: true}, nil
}
