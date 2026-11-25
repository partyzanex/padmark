package mcp

import (
	"net/url"
	"path"
	"time"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// Output conversion: ogen response structs (with their Opt*/enum/url.URL types) → the plain
// tool-output structs. Plain types only — the MCP SDK infers the output JSON Schema from the
// Out types, so they must stay free of custom-marshaling shapes.

// noteOutputFromCreate builds the create output with the edit code hidden. The caller calls
// revealEditCode when the input opted into revealing this one-time secret. The vault copy is
// stored either way.
func (s *Server) noteOutputFromCreate(resp *padmark.CreateNoteResponse) noteOutput {
	out := noteOutput{
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
		ShowContents:     resp.ShowContents.Or(false),
	}
	if resp.Privacy.Set {
		out.Privacy = string(resp.Privacy.Value)
	}

	code := resp.EditCode
	if code != "" {
		out.hideEditCode()
	}

	s.vault.Put(resp.ID, code)

	return out
}

func noteOutputFromNote(resp *padmark.NoteResponse) noteOutput {
	out := noteOutput{
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
		ShowContents:     resp.ShowContents.Or(false),
	}
	if resp.Privacy.Set {
		out.Privacy = string(resp.Privacy.Value)
	}

	return out
}

// linkOutputFromCreate builds the create output with the edit code hidden, the same contract as
// noteOutputFromCreate. The caller calls revealEditCode to reveal the code.
func (s *Server) linkOutputFromCreate(headers *padmark.CreateLinkResponseHeaders) linkOutput {
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

	s.vault.Put(resp.ID, code)

	return out
}

func linkOutputFromLink(resp *padmark.LinkResponse) linkOutput {
	// The generated decoder leaves a "#fragment" in the path, and String would write "%23".
	target := padmark.SplitFragment(resp.TargetURL)

	out := linkOutput{
		ID:             resp.ID,
		TargetURL:      target.String(),
		Views:          resp.Views,
		BurnAfterClick: resp.BurnAfterClick,
		CreatedAt:      timeText(resp.CreatedAt),
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
func (s *Server) bulkLinkOutput(resp *padmark.BulkCreateLinksResponse) createBulkLinksOutput {
	out := createBulkLinksOutput{Results: make([]bulkLinkResultOutput, 0, len(resp.Results))}

	for idx := range resp.Results {
		item := resp.Results[idx]
		// The generated decoder leaves a "#fragment" in the path, and String would write "%23".
		target := padmark.SplitFragment(item.TargetURL)
		row := bulkLinkResultOutput{
			TargetURL: target.String(),
			ID:        "",
			Error:     "",
		}

		if item.Error.Set {
			row.Error = item.Error.Value
		} else {
			row.ID = item.ID.Value
			s.vault.Put(row.ID, item.EditCode.Value)
		}

		out.Results = append(out.Results, row)
	}

	return out
}

// fileOutput is the result of the file tools. The tools return links, not the content: an
// agent gives the link to a person or downloads it with its own HTTP tool.
type fileOutput struct {
	ID                string `json:"id"`
	URL               string `json:"url"`
	DownloadURL       string `json:"download_url"`
	Name              string `json:"name"`
	Privacy           string `json:"privacy,omitempty"`
	ExpiresAt         string `json:"expires_at,omitempty"`
	CreatedAt         string `json:"created_at,omitempty"`
	EditCode          string `json:"edit_code,omitempty"`
	EditCodeNote      string `json:"edit_code_note,omitempty"`
	Size              int64  `json:"size"`
	Downloads         int    `json:"downloads"`
	BurnAfterDownload bool   `json:"burn_after_download"`
}

// fileOutputFromCreate builds the upload output with the edit code hidden, the same contract as
// noteOutputFromCreate. The caller calls revealEditCode to reveal the code.
func (s *Server) fileOutputFromCreate(resp *padmark.CreateFileResponse) fileOutput {
	out := fileOutputFromInfo(&padmark.FileInfo{
		ID:                resp.ID,
		URL:               resp.URL,
		Name:              resp.Name,
		Size:              resp.Size,
		Privacy:           padmark.FileInfoPrivacy(resp.Privacy),
		Downloads:         resp.Downloads,
		BurnAfterDownload: resp.BurnAfterDownload,
		ExpiresAt:         resp.ExpiresAt,
		CreatedAt:         resp.CreatedAt,
	})

	code := resp.EditCode
	if code != "" {
		out.EditCodeNote = "edit_code stored by the proxy for this session; update_file and delete_file " +
			"need no edit_code now"
	}

	s.vault.Put(resp.ID, code)

	return out
}

func fileOutputFromInfo(info *padmark.FileInfo) fileOutput {
	return fileOutput{
		ID:                info.ID,
		URL:               info.URL.String(),
		DownloadURL:       fileDownloadURL(&info.URL, info.ID),
		Name:              info.Name,
		Privacy:           string(info.Privacy),
		ExpiresAt:         nilTimeText(info.ExpiresAt),
		CreatedAt:         timeText(info.CreatedAt),
		EditCode:          "",
		EditCodeNote:      "",
		Size:              info.Size,
		Downloads:         info.Downloads,
		BurnAfterDownload: info.BurnAfterDownload,
	}
}

// fileDownloadURL derives <server>/files/{id}/download from the page URL <server>/{id}. The
// server sends only the page URL; the download path is part of the API contract.
func fileDownloadURL(page *url.URL, id string) string {
	download := *page
	download.Path = path.Join(path.Dir(page.Path), "files", id, "download")
	download.RawPath = ""

	return download.String()
}

// revealEditCode shows the one-time edit code of a file upload output.
func (out *fileOutput) revealEditCode(code string) {
	out.EditCode = code
	out.EditCodeNote = revealedEditCodeNote
}

// revealedEditCodeNote tells the caller that a revealed edit code cannot be recovered later.
const revealedEditCodeNote = "edit_code shown by opt-in request — it cannot be recovered later"

// hideEditCode clears the edit code of a note create output. The hint says that the proxy holds
// the code.
func (out *noteOutput) hideEditCode() {
	out.EditCode = ""
	out.EditCodeNote = "edit_code stored by the proxy for this session; update/delete need no edit_code now"
}

// revealEditCode shows the one-time edit code of a note create output.
func (out *noteOutput) revealEditCode(code string) {
	out.EditCode = code
	out.EditCodeNote = revealedEditCodeNote
}

// hideEditCode clears the edit code of a link create output. The hint says that the proxy holds
// the code.
func (out *linkOutput) hideEditCode() {
	out.EditCode = ""
	out.EditCodeNote = "edit_code stored by the proxy for this session; update_link and delete_link need no " +
		"edit_code now"
}

// revealEditCode shows the one-time edit code of a link create output.
func (out *linkOutput) revealEditCode(code string) {
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
