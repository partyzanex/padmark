package mcp

import (
	"context"
	"fmt"
	"io"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/partyzanex/padmark/internal/errorcontext"
	padmark "github.com/partyzanex/padmark/pkg/client"
)

//go:generate go run go.uber.org/mock/mockgen@latest -source=files.go -destination=files_mocks_test.go -package=mcp -write_package_comment=false

// FileClient is the part of the padmark client that the file tools need. It is separate from
// PadmarkClient, so that each seam has a small set of methods. *padmark.Client implements both.
// Errors are fail-hard for each call: the tool returns the error to the agent as its result.
type FileClient interface {
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

var _ FileClient = (*padmark.Client)(nil)

// registerFileTools adds the file tools to the server. There is no download tool: the tools
// return the page and download URLs, and an agent or a person opens them directly. The tool
// result would otherwise carry a file of any size and type through the model context.
func registerFileTools(srv *mcpsdk.Server, m *Manager) {
	mcpsdk.AddTool[uploadFileInput, fileOutput](srv, uploadFileTool(), m.uploadFile)
	mcpsdk.AddTool[getFileInput, fileOutput](srv, getFileTool(), m.getFile)
	mcpsdk.AddTool[updateFileInput, fileOutput](srv, updateFileTool(), m.updateFile)
	mcpsdk.AddTool[deleteFileInput, deleteNoteOutput](srv, deleteFileTool(), m.deleteFile)
}

func uploadFileTool() *mcpsdk.Tool {
	return newTool("upload_file",
		"Upload a local file and get its page URL and download URL. The path must be a regular file "+
			"under the upload root that the operator set with --upload-root; a relative path is relative "+
			"to that root. The edit_code is returned once and the proxy keeps it for this session. The "+
			"result shows it only when reveal_edit_code is set.",
		hints(false, false, false))
}

func getFileTool() *mcpsdk.Tool {
	return newTool("get_file",
		"Read file metadata: name, size, privacy, expiry and URLs. Read-only: this does not download "+
			"the file, does not count a download, and does not delete a burn-after-download file.",
		hints(true, false, false))
}

func updateFileTool() *mcpsdk.Tool {
	return newTool("update_file",
		"Change the expiry, privacy or burn-after-download setting of a file. The content does not "+
			"change. ttl counts from now and 0 expires the file now; clear_expiry removes the expiry. "+
			"The edit_code comes from the input, else from the proxy's session vault, else from the "+
			"bearer token.",
		// Not idempotent: a repeated ttl moves the expiry, because ttl counts from the call.
		hints(false, false, false))
}

func deleteFileTool() *mcpsdk.Tool {
	return newTool("delete_file",
		"Delete a file permanently. The edit_code comes from the input, else from the proxy's session "+
			"vault, else from the bearer token.",
		hints(false, true, false))
}

func (m *Manager) uploadFile(
	ctx context.Context, _ *mcpsdk.CallToolRequest, input uploadFileInput,
) (*mcpsdk.CallToolResult, fileOutput, error) {
	file, name, err := openUploadFile(m.uploadRoot, input.Path)
	if err != nil {
		return nil, fileOutput{}, fmt.Errorf("open upload file: %w", err)
	}

	defer func() {
		// The file is read-only, so a close error loses no data and does not change the result.
		closeErr := file.Close()
		if closeErr != nil {
			m.log.WarnContext(ctx, "close upload file", "err", closeErr)
		}
	}()

	if input.Name != nil && *input.Name != "" {
		name = *input.Name
	}

	resp, err := m.files.UploadFile(ctx, name, file, uploadOptions(input))
	if err != nil {
		return nil, fileOutput{}, uploadError(err)
	}

	out := m.fileOutputFromCreate(resp)
	if input.RevealEditCode && resp.EditCode != "" {
		out.revealEditCode(resp.EditCode)
	}

	return nil, out, nil
}

// uploadOptions converts the tool input into client options. Unset fields stay at their zero
// values, so the server applies its defaults.
func uploadOptions(input uploadFileInput) padmark.UploadFileOptions {
	opts := padmark.UploadFileOptions{
		TTL: 0, Privacy: "", Slug: "", EditCode: "", BurnAfterDownload: false,
	}
	if input.TTL != nil {
		opts.TTL = time.Duration(*input.TTL) * time.Second
	}

	if input.Privacy != nil {
		opts.Privacy = *input.Privacy
	}

	if input.Slug != nil {
		opts.Slug = *input.Slug
	}

	if input.EditCode != nil {
		opts.EditCode = *input.EditCode
	}

	if input.BurnAfterDownload != nil {
		opts.BurnAfterDownload = *input.BurnAfterDownload
	}

	return opts
}

func (m *Manager) getFile(
	ctx context.Context, _ *mcpsdk.CallToolRequest, input getFileInput,
) (*mcpsdk.CallToolResult, fileOutput, error) {
	res, err := m.files.GetFile(ctx, padmark.GetFileParams{ID: input.ID})
	if err != nil {
		return nil, fileOutput{}, errorcontext.Wrap(transportError("get file", err), "handle get file")
	}

	info, apiErr := mapGetFileRes(res)
	if apiErr != nil {
		return nil, fileOutput{}, fmt.Errorf("map get file res: %w", apiErr)
	}

	return nil, fileOutputFromInfo(info), nil
}

func (m *Manager) updateFile(
	ctx context.Context, _ *mcpsdk.CallToolRequest, input updateFileInput,
) (*mcpsdk.CallToolResult, fileOutput, error) {
	req, err := newUpdateFileRequest(input)
	if err != nil {
		return nil, fileOutput{}, err
	}

	res, err := m.files.UpdateFile(ctx, req, padmark.UpdateFileParams{
		ID:        input.ID,
		XEditCode: editCodeParam(m.vault, input.ID, input.EditCode),
	})
	if err != nil {
		return nil, fileOutput{}, errorcontext.Wrap(transportError("update file", err), "handle update file")
	}

	info, apiErr := mapUpdateFileRes(res)
	if apiErr != nil {
		return nil, fileOutput{}, fmt.Errorf("map update file res: %w", apiErr)
	}

	return nil, fileOutputFromInfo(info), nil
}

// newUpdateFileRequest builds the PUT body. A JSON null for ttl removes the expiry, but the tool
// input cannot tell null from an absent field, so clear_expiry sends the null. An empty change is
// a model-fixable mistake, so the tool refuses it before a server call.
func newUpdateFileRequest(input updateFileInput) (*padmark.UpdateFileRequest, error) {
	req := &padmark.UpdateFileRequest{
		TTL:               padmark.OptNilInt64{Value: 0, Set: false, Null: false},
		Privacy:           padmark.OptUpdateFileRequestPrivacy{Value: "", Set: false},
		BurnAfterDownload: padmark.OptBool{Value: false, Set: false},
	}

	if input.ClearExpiry && input.TTL != nil {
		return nil, errTTLWithClearExpiry
	}

	if input.ClearExpiry {
		req.TTL = padmark.OptNilInt64{Value: 0, Set: true, Null: true}
	}

	if input.TTL != nil {
		req.TTL = padmark.NewOptNilInt64(*input.TTL)
	}

	if input.Privacy != nil {
		req.Privacy = padmark.NewOptUpdateFileRequestPrivacy(padmark.UpdateFileRequestPrivacy(*input.Privacy))
	}

	if input.BurnAfterDownload != nil {
		req.BurnAfterDownload = padmark.NewOptBool(*input.BurnAfterDownload)
	}

	if !req.TTL.Set && !req.Privacy.Set && !req.BurnAfterDownload.Set {
		return nil, errNoFileChange
	}

	return req, nil
}

func (m *Manager) deleteFile(
	ctx context.Context, _ *mcpsdk.CallToolRequest, input deleteFileInput,
) (*mcpsdk.CallToolResult, deleteNoteOutput, error) {
	res, err := m.files.DeleteFile(ctx, padmark.DeleteFileParams{
		ID:        input.ID,
		XEditCode: editCodeParam(m.vault, input.ID, input.EditCode),
	})
	if err != nil {
		return nil, deleteNoteOutput{}, errorcontext.Wrap(transportError("delete file", err), "handle delete file")
	}

	apiErr := mapDeleteFileRes(res)
	if apiErr != nil {
		return nil, deleteNoteOutput{}, fmt.Errorf("map delete file res: %w", apiErr)
	}

	return nil, deleteNoteOutput{ID: input.ID, Deleted: true}, nil
}

// editCodeParam returns the X-Edit-Code header value for a file mutation. Files take the edit
// code only from the header. Without a code, the header is not sent, so the server can use the
// bearer token of the owner.
func editCodeParam(vault *editCodeVault, id string, explicit *string) padmark.OptString {
	if code := editCodeFor(vault, id, explicit); code != "" {
		return padmark.NewOptString(code)
	}

	return padmark.OptString{Value: "", Set: false}
}
