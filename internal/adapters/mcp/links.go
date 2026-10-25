package mcp

import (
	"context"
	"fmt"
	"net/url"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/partyzanex/padmark/internal/errorcontext"
	padmark "github.com/partyzanex/padmark/pkg/client"
)

// registerLinkTools adds the short-link tools to the server.
func registerLinkTools(srv *mcpsdk.Server, m *Manager) {
	mcpsdk.AddTool[createLinkInput, createLinkOutput](srv, createLinkTool(), m.createLink)
	mcpsdk.AddTool[createBulkLinksInput, createBulkLinksOutput](srv, createLinksBulkTool(), m.createLinksBulk)
	mcpsdk.AddTool[getLinkInput, createLinkOutput](srv, getLinkTool(), m.getLink)
	mcpsdk.AddTool[deleteLinkInput, deleteNoteOutput](srv, deleteLinkTool(), m.deleteLink)
}

func createLinkTool() *mcpsdk.Tool {
	return newTool("create_link",
		"Create a short link to a target URL (http/https only). Browsers follow it at GET /r/{id}; the padmark "+
			"edit_code is returned exactly once and is kept in the proxy for delete within this session; it is shown "+
			"in the result only when reveal_edit_code is set.",
		hints(false, false, false))
}

func createLinksBulkTool() *mcpsdk.Tool {
	return newTool("create_links_bulk",
		"Create one to one hundred short links in order, best-effort: each result item reports its own id or "+
			"error, and a failing item does not stop the others.",
		hints(false, false, false))
}

func getLinkTool() *mcpsdk.Tool {
	return newTool("get_link",
		"Read short-link metadata. Read-only: this does not follow the link, bump views, or consume "+
			"burn-after-click — following happens on the browser-facing GET /r/{id} path.",
		hints(true, false, false))
}

func deleteLinkTool() *mcpsdk.Tool {
	return newTool("delete_link",
		"Delete a short link permanently. The edit_code comes from the input, else from the proxy's session "+
			"vault, else from the bearer token.",
		hints(false, true, false))
}

func (m *Manager) createLink(
	ctx context.Context, _ *mcpsdk.CallToolRequest, input createLinkInput,
) (*mcpsdk.CallToolResult, createLinkOutput, error) {
	// A bad target URL is a model-fixable mistake; fail up front instead of a 400 round-trip.
	parsed, err := url.Parse(input.TargetURL)
	if err != nil || (parsed.Host == "" && parsed.Scheme == "") {
		return nil, createLinkOutput{}, fmt.Errorf("target_url %q %w", input.TargetURL, errInvalidTargetURL)
	}

	req := newCreateLinkRequest(input, parsed)

	res, err := m.client.CreateLink(ctx, req)
	if err != nil {
		return nil, createLinkOutput{}, errorcontext.Wrap(transportError("create link", err), "handle create link")
	}

	resp, apiErr := mapCreateLinkRes(res)
	if apiErr != nil {
		return nil, createLinkOutput{}, fmt.Errorf("map create link res: %w", apiErr)
	}

	return nil, m.linkOutputFromCreate(resp, input.RevealEditCode), nil
}

// newCreateLinkRequest converts the parsed tool input into an ogen request, leaving optional
// fields unset so the server applies its defaults.
func newCreateLinkRequest(input createLinkInput, parsed *url.URL) *padmark.CreateLinkRequest {
	req := &padmark.CreateLinkRequest{
		TargetURL:      *parsed,
		Slug:           padmark.OptString{Value: "", Set: false},
		BurnAfterClick: padmark.OptBool{Value: false, Set: false},
		TTL:            padmark.OptInt64{Value: 0, Set: false},
		EditCode:       padmark.OptString{Value: "", Set: false},
		Privacy:        padmark.OptCreateLinkRequestPrivacy{Value: "", Set: false},
		DirectRedirect: padmark.OptNilBool{Value: false, Set: false, Null: false},
	}
	if input.Slug != nil {
		req.Slug = padmark.NewOptString(*input.Slug)
	}

	if input.BurnAfterClick != nil {
		req.BurnAfterClick = padmark.NewOptBool(*input.BurnAfterClick)
	}

	if input.TTL != nil {
		req.TTL = padmark.NewOptInt64(*input.TTL)
	}

	if input.Privacy != nil {
		req.Privacy = padmark.NewOptCreateLinkRequestPrivacy(
			padmark.CreateLinkRequestPrivacy(*input.Privacy))
	}

	if input.DirectRedirect != nil {
		req.DirectRedirect = padmark.OptNilBool{Value: *input.DirectRedirect, Set: true, Null: false}
	}

	if input.EditCode != nil {
		req.EditCode = padmark.NewOptString(*input.EditCode)
	}

	return req
}

func (m *Manager) createLinksBulk(
	ctx context.Context, _ *mcpsdk.CallToolRequest, input createBulkLinksInput,
) (*mcpsdk.CallToolResult, createBulkLinksOutput, error) {
	if len(input.TargetURLs) == 0 {
		return nil, createBulkLinksOutput{}, errEmptyTargetURLs
	}

	// The padmark contract caps bulk creation at one hundred links per request.
	const bulkLimit = 100
	if len(input.TargetURLs) > bulkLimit {
		return nil, createBulkLinksOutput{},
			fmt.Errorf("%w the %d-links padmark limit", errTooManyTargets, bulkLimit)
	}

	reqs, buildErr := bulkCreateRequests(input.TargetURLs)
	if buildErr != nil {
		return nil, createBulkLinksOutput{}, fmt.Errorf("bulk create requests: %w", buildErr)
	}

	res, err := m.client.CreateLinksBulk(ctx, &padmark.BulkCreateLinksRequest{Links: reqs})
	if err != nil {
		return nil, createBulkLinksOutput{}, errorcontext.Wrap(
			transportError("bulk create links", err), "create bulk links",
		)
	}

	resp, apiErr := mapCreateLinksBulkRes(res)
	if apiErr != nil {
		return nil, createBulkLinksOutput{}, fmt.Errorf("map create links bulk res: %w", apiErr)
	}

	return nil, m.bulkLinkOutput(resp), nil
}

// bulkCreateRequests parses every target URL up front, reporting the first failure with the
// offending index so the model can fix exactly that item.
func bulkCreateRequests(targetURLs []string) ([]padmark.CreateLinkRequest, error) {
	// exhaustruct wants a fully-filled literal per item; only TargetURL changes per item, so
	// the all-unset request is built once and copied.
	empty := padmark.CreateLinkRequest{
		TargetURL: url.URL{
			Scheme: "", Opaque: "", User: nil, Host: "", Path: "", Fragment: "",
			RawQuery: "", RawPath: "", RawFragment: "", ForceQuery: false, OmitHost: false,
		},
		Slug:           padmark.OptString{Value: "", Set: false},
		BurnAfterClick: padmark.OptBool{Value: false, Set: false},
		TTL:            padmark.OptInt64{Value: 0, Set: false},
		EditCode:       padmark.OptString{Value: "", Set: false},
		Privacy:        padmark.OptCreateLinkRequestPrivacy{Value: "", Set: false},
		DirectRedirect: padmark.OptNilBool{Value: false, Set: false, Null: false},
	}

	reqs := make([]padmark.CreateLinkRequest, 0, len(targetURLs))

	for idx, targetURL := range targetURLs {
		parsed, err := url.Parse(targetURL)
		if err != nil || (parsed.Host == "" && parsed.Scheme == "") {
			return nil, fmt.Errorf("target_urls[%d] %q %w", idx, targetURL, errInvalidTargetURL)
		}

		req := empty
		req.TargetURL = *parsed
		reqs = append(reqs, req)
	}

	return reqs, nil
}

func (m *Manager) getLink(
	ctx context.Context, _ *mcpsdk.CallToolRequest, input getLinkInput,
) (*mcpsdk.CallToolResult, createLinkOutput, error) {
	res, err := m.client.GetLink(ctx, padmark.GetLinkParams{ID: input.ID})
	if err != nil {
		return nil, createLinkOutput{}, errorcontext.Wrap(transportError("get link", err), "handle get link")
	}

	resp, apiErr := mapGetLinkRes(res)
	if apiErr != nil {
		return nil, createLinkOutput{}, fmt.Errorf("map get link res: %w", apiErr)
	}

	return nil, linkOutputFromLink(resp), nil
}

func (m *Manager) deleteLink(
	ctx context.Context, _ *mcpsdk.CallToolRequest, input deleteLinkInput,
) (*mcpsdk.CallToolResult, deleteNoteOutput, error) {
	params := padmark.DeleteLinkParams{
		ID:        input.ID,
		XEditCode: padmark.OptString{Value: "", Set: false},
	}
	if code := editCodeFor(m.vault, input.ID, input.EditCode); code != "" {
		// The header, not the query parameter — query strings land in access logs.
		params.XEditCode = padmark.NewOptString(code)
	}

	res, err := m.client.DeleteLink(ctx, params)
	if err != nil {
		return nil, deleteNoteOutput{}, errorcontext.Wrap(transportError("delete link", err), "handle delete link")
	}

	apiErr := mapDeleteLinkRes(res)
	if apiErr != nil {
		return nil, deleteNoteOutput{}, fmt.Errorf("map delete link res: %w", apiErr)
	}

	return nil, deleteNoteOutput{ID: input.ID, Deleted: true}, nil
}
