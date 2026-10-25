package mcp

import (
	"time"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// Output conversion: ogen response structs (with their Opt*/enum/url.URL types) → the plain
// tool-output structs. Plain types only — the MCP SDK infers the output JSON Schema from the
// Out types, so they must stay free of custom-marshaling shapes.

// noteOutputFromCreate includes the edit code: hidden unless the caller opted into revealing
// this one-time secret; the vault copy is stored either way.
func (m *Manager) noteOutputFromCreate(resp *padmark.CreateNoteResponse, reveal bool) createNoteOutput {
	out := createNoteOutput{
		ID:               resp.ID,
		Title:            resp.Title,
		Content:          resp.Content,
		ContentType:      string(resp.ContentType),
		Views:            resp.Views,
		BurnAfterReading: resp.BurnAfterReading,
		CreatedAt:        timeText(resp.CreatedAt),
		UpdatedAt:        timeText(resp.UpdatedAt),
		ExpiresAt:        nilTimeText(resp.ExpiresAt),
		Privacy:          "",
		EditCode:         "",
		EditCodeNote:     "",
	}
	if resp.Privacy.Set {
		out.Privacy = string(resp.Privacy.Value)
	}

	code := resp.EditCode
	if code != "" {
		out.editCodeFields(code, reveal)
	}

	m.vault.Put(resp.ID, code)

	return out
}

func noteOutputFromNote(resp *padmark.NoteResponse) createNoteOutput {
	out := createNoteOutput{
		ID:               resp.ID,
		Title:            resp.Title,
		Content:          resp.Content,
		ContentType:      string(resp.ContentType),
		Views:            resp.Views,
		BurnAfterReading: resp.BurnAfterReading,
		CreatedAt:        timeText(resp.CreatedAt),
		UpdatedAt:        timeText(resp.UpdatedAt),
		ExpiresAt:        nilTimeText(resp.ExpiresAt),
		Privacy:          "",
		EditCode:         "",
		EditCodeNote:     "",
	}
	if resp.Privacy.Set {
		out.Privacy = string(resp.Privacy.Value)
	}

	return out
}

// linkOutputFromCreate includes the edit code, same reveal contract as notes.
func (m *Manager) linkOutputFromCreate(headers *padmark.CreateLinkResponseHeaders, reveal bool) createLinkOutput {
	resp := headers.Response

	out := linkOutputFromLink(&padmark.LinkResponse{
		ID:             resp.ID,
		TargetURL:      resp.TargetURL,
		Views:          resp.Views,
		BurnAfterClick: resp.BurnAfterClick,
		Privacy: padmark.NewOptLinkResponsePrivacy(
			padmark.LinkResponsePrivacy(resp.Privacy.Value)),
		ExpiresAt:      resp.ExpiresAt,
		DirectRedirect: resp.DirectRedirect,
		CreatedAt:      resp.CreatedAt,
	})
	out.RedirectTo = optStrText(headers.Location)

	code := resp.EditCode
	if code != "" {
		out.editCodeFields(code, reveal)
	}

	m.vault.Put(resp.ID, code)

	return out
}

func linkOutputFromLink(resp *padmark.LinkResponse) createLinkOutput {
	out := createLinkOutput{
		ID:             resp.ID,
		TargetURL:      resp.TargetURL.String(),
		Views:          resp.Views,
		BurnAfterClick: resp.BurnAfterClick,
		CreatedAt:      "",
		ExpiresAt:      nilTimeText(resp.ExpiresAt),
		Privacy:        "",
		DirectRedirect: nil,
		RedirectTo:     "",
		EditCode:       "",
		EditCodeNote:   "",
	}
	if resp.Privacy.Set {
		out.Privacy = string(resp.Privacy.Value)
	}

	if resp.DirectRedirect.Set && !resp.DirectRedirect.Null {
		direct := resp.DirectRedirect.Value
		out.DirectRedirect = &direct
	}

	return out
}

// bulkLinkOutput maps best-effort bulk results item by item; successful items have their one-time
// edit codes stored in the vault (never printed) so delete_link can act on them later.
func (m *Manager) bulkLinkOutput(resp *padmark.BulkCreateLinksResponse) createBulkLinksOutput {
	out := createBulkLinksOutput{Results: make([]bulkLinkResultOutput, 0, len(resp.Results))}

	for idx := range resp.Results {
		item := resp.Results[idx]
		row := bulkLinkResultOutput{
			TargetURL: item.TargetURL.String(),
			ID:        "",
			EditCode:  "",
			Error:     "",
		}

		if item.Error.Set {
			row.Error = item.Error.Value
		} else {
			row.ID = item.ID.Value
			m.vault.Put(row.ID, item.EditCode.Value)
		}

		out.Results = append(out.Results, row)
	}

	return out
}

// editCodeFields fills the edit-code fields of a create output per the reveal contract:
// by default only a hint that the proxy holds the code is shown.
func (out *createNoteOutput) editCodeFields(code string, reveal bool) {
	if reveal {
		out.EditCode = code
		out.EditCodeNote = "edit_code shown by opt-in request — it cannot be recovered later"

		return
	}

	out.EditCodeNote = "edit_code stored by the proxy for this session; update/delete need no edit_code now"
}

// editCodeFields is the same reveal contract for link outputs.
func (out *createLinkOutput) editCodeFields(code string, reveal bool) {
	if reveal {
		out.EditCode = code
		out.EditCodeNote = "edit_code shown by opt-in request — it cannot be recovered later"

		return
	}

	out.EditCodeNote = "edit_code stored by the proxy for this session; delete_link needs no edit_code now"
}

func optStrText(o padmark.OptString) string {
	if o.Set {
		return o.Value
	}

	return ""
}

// nilTimeText renders a nullable expiry; unset or JSON-null means "never expires".
func nilTimeText(o padmark.OptNilDateTime) string {
	if o.Set && !o.Null && !o.Value.IsZero() {
		return o.Value.Format(time.RFC3339)
	}

	return ""
}
