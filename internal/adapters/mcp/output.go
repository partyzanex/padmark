package mcp

import (
	"time"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// Output conversion: ogen response structs (with their Opt*/enum/url.URL types) → the plain
// tool-output structs. Plain types only — the MCP SDK infers the output JSON Schema from the
// Out types, so they must stay free of custom-marshaling shapes.

// noteOutputFromCreate builds the create output with the edit code hidden. The caller calls
// revealEditCode when the input opted into revealing this one-time secret. The vault copy is
// stored either way.
func (m *Manager) noteOutputFromCreate(resp *padmark.CreateNoteResponse) createNoteOutput {
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
		out.hideEditCode()
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

// linkOutputFromCreate builds the create output with the edit code hidden, the same contract as
// noteOutputFromCreate. The caller calls revealEditCode to reveal the code.
func (m *Manager) linkOutputFromCreate(headers *padmark.CreateLinkResponseHeaders) createLinkOutput {
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
		out.hideEditCode()
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

// revealedEditCodeNote tells the caller that a revealed edit code cannot be recovered later.
const revealedEditCodeNote = "edit_code shown by opt-in request — it cannot be recovered later"

// hideEditCode clears the edit code of a note create output. The hint says that the proxy holds
// the code.
func (out *createNoteOutput) hideEditCode() {
	out.EditCode = ""
	out.EditCodeNote = "edit_code stored by the proxy for this session; update/delete need no edit_code now"
}

// revealEditCode shows the one-time edit code of a note create output.
func (out *createNoteOutput) revealEditCode(code string) {
	out.EditCode = code
	out.EditCodeNote = revealedEditCodeNote
}

// hideEditCode clears the edit code of a link create output. The hint says that the proxy holds
// the code.
func (out *createLinkOutput) hideEditCode() {
	out.EditCode = ""
	out.EditCodeNote = "edit_code stored by the proxy for this session; delete_link needs no edit_code now"
}

// revealEditCode shows the one-time edit code of a link create output.
func (out *createLinkOutput) revealEditCode(code string) {
	out.EditCode = code
	out.EditCodeNote = revealedEditCodeNote
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
