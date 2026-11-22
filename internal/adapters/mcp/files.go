package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// checkTTL is the one ttl check of the tools that check ttl before a server call. A nil ttl is
// not set. minimum is 1 for a new record and 0 for an update, where 0 expires the record now.
// The largest value is padmark.MaxTTLSeconds: a larger one overflows time.Duration.
func checkTTL(ttl *int64, minimum int64, rangeErr error) error {
	if ttl != nil && (*ttl < minimum || *ttl > padmark.MaxTTLSeconds) {
		return rangeErr
	}

	return nil
}

// registerFileTools adds the file tools to the server. There is no download tool: the tools
// return the page and download URLs, and an agent or a person opens them directly. The tool
// result would otherwise carry a file of any size and type through the model context.
//
// upload_file is the only tool without the call deadline (limitCall): it sends a file body of
// any size, so only the response header timeout of the HTTP client limits it. The other file
// tools send and receive small JSON bodies.
func registerFileTools(srv *mcpsdk.Server, proxy *Server) {
	mcpsdk.AddTool[uploadFileInput, fileOutput](srv, uploadFileTool(proxy.uploadDefaultPrivacy()), proxy.uploadFile)
	mcpsdk.AddTool[getFileInput, fileOutput](srv, getFileTool(), limitCall(proxy, proxy.getFile))
	mcpsdk.AddTool[updateFileInput, fileOutput](srv, updateFileTool(), limitCall(proxy, proxy.updateFile))
	mcpsdk.AddTool[deleteFileInput, deletedOutput](srv, deleteFileTool(), limitCall(proxy, proxy.deleteFile))
}

// uploadFileTool tells the agent the default privacy, because a public file is readable by
// everyone who has its URL.
func uploadFileTool(defaultPrivacy string) *mcpsdk.Tool {
	return newTool("upload_file",
		"Upload a local file and get its page URL and download URL. The path must be a regular file "+
			"under the upload root that the operator set with --upload-root; a relative path is relative "+
			"to that root. Names that start with a dot are always refused. "+
			defaultPrivacyText(defaultPrivacy)+" "+
			"Without ttl, the file does not expire. "+
			"The edit_code is returned once and the proxy keeps it for this session. The "+
			"result shows it only when reveal_edit_code is set.",
		additiveTool)
}

// defaultPrivacyText tells the agent who can read an upload without a privacy field.
func defaultPrivacyText(defaultPrivacy string) string {
	if defaultPrivacy == string(padmark.FileInfoPrivacyOwner) {
		return "Without privacy, the privacy is owner: only the account of the proxy token can read the file."
	}

	return "Without privacy, the privacy is public: each person with the URL can read the file."
}

func getFileTool() *mcpsdk.Tool {
	return newTool("get_file",
		"Read file metadata: name, size, privacy, expiry and URLs. Read-only: this does not download "+
			"the file, does not count a download, and does not delete a burn-after-download file.",
		readOnlyTool)
}

func updateFileTool() *mcpsdk.Tool {
	return newTool("update_file",
		"Change the expiry, privacy or burn-after-download setting of a file. The content does not "+
			"change. ttl counts from now and 0 expires the file now; clear_expiry removes the expiry. "+
			"The edit_code comes from the input, else from the proxy's session vault, else from the "+
			"bearer token.",
		// Destructive: ttl 0 deletes the file now. Not idempotent: a repeated ttl moves the
		// expiry, because ttl counts from the call.
		destructiveTool)
}

func deleteFileTool() *mcpsdk.Tool {
	return newTool("delete_file",
		"Delete a file permanently. The edit_code comes from the input, else from the proxy's session "+
			"vault, else from the bearer token.",
		destructiveTool)
}

func (s *Server) uploadFile(
	ctx context.Context, _ *mcpsdk.CallToolRequest, input uploadFileInput,
) (*mcpsdk.CallToolResult, fileOutput, error) {
	opts, err := s.uploadOptions(input)
	if err != nil {
		return nil, fileOutput{}, err
	}

	file, name, err := openUploadFile(s.uploadRoot, input.Path)
	if err != nil {
		return nil, fileOutput{}, fmt.Errorf("open upload file: %w", err)
	}

	defer func() {
		// The file is read-only, so a close error loses no data and does not change the result.
		closeErr := file.Close()
		if closeErr != nil {
			s.log.WarnContext(ctx, "close upload file", "err", closeErr)
		}
	}()

	if input.Name != nil && *input.Name != "" {
		name = *input.Name
	}

	body := &readErrRecorder{reader: file, err: nil, mu: sync.Mutex{}}

	resp, err := s.client.UploadFile(ctx, name, body, opts)
	if err != nil {
		// After an error, UploadFile does not wait for the goroutine that reads body (see its
		// godoc). That goroutine can record a read error at this time, so firstErr takes the lock.
		// A read error that comes later does not change the result: the error of UploadFile was first.
		readErr := body.firstErr()
		if readErr != nil {
			return nil, fileOutput{}, fmt.Errorf("upload file: %w %q: %w", errReadLocalFile, input.Path, readErr)
		}

		return nil, fileOutput{}, uploadError(err)
	}

	out := s.fileOutputFromCreate(resp)
	if input.RevealEditCode && resp.EditCode != "" {
		out.revealEditCode(resp.EditCode)
	}

	return nil, out, nil
}

// readErrRecorder keeps the first read error of the local file. The upload client reports a
// read error as a transport error, and the tool must not tell the agent to check the server.
// The upload client reads the file in its own goroutine, and after an error it does not wait for
// that goroutine. The tool handler can thus read the error while the goroutine writes it, so mu
// guards err.
type readErrRecorder struct {
	reader io.Reader
	err    error
	mu     sync.Mutex
}

func (r *readErrRecorder) Read(buf []byte) (int, error) {
	count, err := r.reader.Read(buf)
	if err == nil {
		return count, nil
	}

	// The io.Reader contract needs io.EOF itself, not a wrapped copy.
	if errors.Is(err, io.EOF) {
		return count, io.EOF
	}

	r.record(err)

	return count, fmt.Errorf("read upload file: %w", err)
}

// record keeps err when it is the first read error.
func (r *readErrRecorder) record(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.err == nil {
		r.err = err
	}
}

// firstErr returns the first read error, or nil when no read failed yet.
func (r *readErrRecorder) firstErr() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.err
}

// uploadDefaultPrivacy is the privacy of an upload without a privacy field. With a token, the
// account owns the file, so "owner" is valid and keeps the file private. Without a token, the
// file has no owner and the server refuses "owner", so the server default "public" stays.
func (s *Server) uploadDefaultPrivacy() string {
	if s.hasToken {
		return string(padmark.FileInfoPrivacyOwner)
	}

	return string(padmark.FileInfoPrivacyPublic)
}

// uploadOptions converts the tool input into client options. It refuses input that the agent
// can correct before the proxy reads the file.
func (s *Server) uploadOptions(input uploadFileInput) (padmark.UploadFileOptions, error) {
	opts := padmark.UploadFileOptions{
		TTL: 0, Privacy: "", Slug: "", EditCode: "", BurnAfterDownload: false,
	}

	// Check the range before the conversion: a larger value overflows time.Duration.
	err := checkTTL(input.TTL, 1, errUploadTTLRange)
	if err != nil {
		return opts, err
	}

	if input.TTL != nil {
		opts.TTL = time.Duration(*input.TTL) * time.Second
	}

	if s.hasToken {
		opts.Privacy = s.uploadDefaultPrivacy()
	}

	if input.Privacy != nil {
		opts.Privacy = *input.Privacy
	}

	if input.Slug != nil && *input.Slug != "" {
		opts.Slug = *input.Slug
	}

	if input.EditCode != nil {
		opts.EditCode = *input.EditCode
	}

	if input.BurnAfterDownload != nil {
		opts.BurnAfterDownload = *input.BurnAfterDownload
	}

	return opts, nil
}

func (s *Server) getFile(
	ctx context.Context, _ *mcpsdk.CallToolRequest, input getFileInput,
) (*mcpsdk.CallToolResult, fileOutput, error) {
	res, err := s.client.GetFile(ctx, padmark.GetFileParams{ID: input.ID})

	info, err := fetch[*padmark.FileInfo](res, err, "get file", operation{
		context: "decode get file response", rateScope: "file reads", kind: fileRecord(),
	})
	if err != nil {
		return nil, fileOutput{}, err
	}

	return nil, fileOutputFromInfo(info), nil
}

func (s *Server) updateFile(
	ctx context.Context, _ *mcpsdk.CallToolRequest, input updateFileInput,
) (*mcpsdk.CallToolResult, fileOutput, error) {
	req, err := newUpdateFileRequest(input)
	if err != nil {
		return nil, fileOutput{}, err
	}

	res, err := s.client.UpdateFile(ctx, req, padmark.UpdateFileParams{
		ID:        input.ID,
		XEditCode: editCodeParam(s.vault, input.ID, input.EditCode),
	})

	info, err := fetch[*padmark.FileInfo](res, err, "update file", operation{
		context: "decode update file response", rateScope: "file updates", kind: fileRecord(),
	})
	if err != nil {
		return nil, fileOutput{}, err
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

	ttl, err := updateTTL(input)
	if err != nil {
		return nil, err
	}

	req.TTL = ttl

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

// updateTTL returns the ttl field of the PUT body: unset, null (clear_expiry) or a value. 0 is
// valid and expires the file now.
func updateTTL(input updateFileInput) (padmark.OptNilInt64, error) {
	if input.ClearExpiry && input.TTL != nil {
		return padmark.OptNilInt64{Value: 0, Set: false, Null: false}, errTTLWithClearExpiry
	}

	if input.ClearExpiry {
		return padmark.OptNilInt64{Value: 0, Set: true, Null: true}, nil
	}

	err := checkTTL(input.TTL, 0, errUpdateTTLRange)
	if err != nil || input.TTL == nil {
		return padmark.OptNilInt64{Value: 0, Set: false, Null: false}, err
	}

	return padmark.NewOptNilInt64(*input.TTL), nil
}

func (s *Server) deleteFile(
	ctx context.Context, _ *mcpsdk.CallToolRequest, input deleteFileInput,
) (*mcpsdk.CallToolResult, deletedOutput, error) {
	res, err := s.client.DeleteFile(ctx, padmark.DeleteFileParams{
		ID:        input.ID,
		XEditCode: editCodeParam(s.vault, input.ID, input.EditCode),
	})

	_, err = fetch[*padmark.DeleteFileNoContent](res, err, "delete file", operation{
		context: "decode delete file response", rateScope: "file deletes", kind: fileRecord(),
	})
	if err != nil {
		return nil, deletedOutput{}, err
	}

	return nil, deletedOutput{ID: input.ID, Deleted: true}, nil
}

// editCodeParam returns the edit code of a mutation: the X-Edit-Code header of a file or a
// delete, or the edit_code field of update_link. Without a code, the value is not sent, so the
// server can use the bearer token of the owner.
func editCodeParam(vault *editCodeVault, id string, explicit *string) padmark.OptString {
	if code := editCodeFor(vault, id, explicit); code != "" {
		return padmark.NewOptString(code)
	}

	return padmark.OptString{Value: "", Set: false}
}
