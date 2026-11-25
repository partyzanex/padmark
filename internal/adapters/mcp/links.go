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
func registerLinkTools(srv *mcpsdk.Server, proxy *Server) {
	mcpsdk.AddTool[createLinkInput, linkOutput](srv, createLinkTool(), limitCall(proxy, proxy.createLink))
	mcpsdk.AddTool[createBulkLinksInput, createBulkLinksOutput](
		srv, createLinksBulkTool(), limitCall(proxy, proxy.createLinksBulk),
	)
	mcpsdk.AddTool[getLinkInput, linkOutput](srv, getLinkTool(), limitCall(proxy, proxy.getLink))
	mcpsdk.AddTool[updateLinkInput, linkOutput](srv, updateLinkTool(), limitCall(proxy, proxy.updateLink))
	mcpsdk.AddTool[deleteLinkInput, deletedOutput](srv, deleteLinkTool(), limitCall(proxy, proxy.deleteLink))
}

func createLinkTool() *mcpsdk.Tool {
	return newTool("create_link",
		"Create a short link to a target URL (http/https only). Browsers follow it at GET /{id}; the padmark "+
			"edit_code is returned exactly once and is kept in the proxy for update_link and delete_link within this "+
			"session; it is shown in the result only when reveal_edit_code is set.",
		additiveTool)
}

func createLinksBulkTool() *mcpsdk.Tool {
	return newTool("create_links_bulk",
		"Create one to one hundred short links in order, best-effort: each result item reports its own id or "+
			"error, and a failing item does not stop the others.",
		additiveTool)
}

func getLinkTool() *mcpsdk.Tool {
	return newTool("get_link",
		"Read short-link metadata. Read-only: this does not follow the link, bump views, or consume "+
			"burn-after-click — following happens on the browser-facing GET /{id} path.",
		readOnlyTool)
}

func updateLinkTool() *mcpsdk.Tool {
	return newTool("update_link",
		"Replace a short link's destination (like the REST PUT). An omitted optional field keeps its value. "+
			"ttl counts from now and 0 expires the link now. The edit_code comes from the input, else from "+
			"the proxy's session vault, else from the bearer token (the link's owner may omit the code).",
		// Destructive: the old destination is replaced, and ttl 0 expires the link now. Not
		// idempotent: a repeated ttl moves the expiry, because ttl counts from the call.
		destructiveTool)
}

func deleteLinkTool() *mcpsdk.Tool {
	return newTool("delete_link",
		"Delete a short link permanently. The edit_code comes from the input, else from the proxy's session "+
			"vault, else from the bearer token.",
		destructiveTool)
}

func (s *Server) createLink(
	ctx context.Context, _ *mcpsdk.CallToolRequest, input createLinkInput,
) (*mcpsdk.CallToolResult, linkOutput, error) {
	parsed, err := parseTargetURL(input.TargetURL)
	if err != nil {
		return nil, linkOutput{}, err
	}

	req := newCreateLinkRequest(input, parsed)

	res, err := s.client.CreateLink(ctx, req)

	resp, err := fetch[*padmark.CreateLinkResponseHeaders](res, err, "create link", operation{
		context: "decode create link response", rateScope: "link creations", kind: linkRecord(),
	})
	if err != nil {
		return nil, linkOutput{}, err
	}

	out := s.linkOutputFromCreate(resp)
	if input.RevealEditCode && resp.Response.EditCode != "" {
		out.revealEditCode(resp.Response.EditCode)
	}

	return nil, out, nil
}

// parseTargetURL refuses a target URL that is not an absolute address. A bad target URL is a
// model-fixable mistake, so the tool fails before a 400 round-trip.
func parseTargetURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Host == "" && parsed.Scheme == "") {
		return nil, fmt.Errorf("target_url %q %w", raw, errInvalidTargetURL)
	}

	return parsed, nil
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

func (s *Server) createLinksBulk(
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

	res, err := s.client.CreateLinksBulk(ctx, &padmark.BulkCreateLinksRequest{Links: reqs})
	if err != nil {
		return nil, createBulkLinksOutput{}, errorcontext.Wrap(
			transportError("bulk create links", err), "create bulk links",
		)
	}

	resp, err := successOf[*padmark.BulkCreateLinksResponse](res, "map create links bulk res", operation{
		context: "decode create links bulk response", rateScope: "bulk link creations", kind: linkRecord(),
	}, nil)
	if err != nil {
		return nil, createBulkLinksOutput{}, err
	}

	return nil, s.bulkLinkOutput(resp), nil
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

func (s *Server) getLink(
	ctx context.Context, _ *mcpsdk.CallToolRequest, input getLinkInput,
) (*mcpsdk.CallToolResult, linkOutput, error) {
	res, err := s.client.GetLink(ctx, padmark.GetLinkParams{ID: input.ID})

	link, err := fetch[*padmark.LinkResponse](res, err, "get link", operation{
		context: "decode get link response", rateScope: "link reads", kind: linkRecord(),
	})
	if err != nil {
		return nil, linkOutput{}, err
	}

	return nil, linkOutputFromLink(link), nil
}

// updateLink replaces the destination of a link, as update_note replaces the content of a note.
// An unset optional field is not sent, so the server keeps its value.
func (s *Server) updateLink(
	ctx context.Context, _ *mcpsdk.CallToolRequest, input updateLinkInput,
) (*mcpsdk.CallToolResult, linkOutput, error) {
	req, err := newUpdateLinkRequest(input)
	if err != nil {
		return nil, linkOutput{}, err
	}

	req.EditCode = editCodeParam(s.vault, input.ID, input.EditCode)

	res, err := s.client.UpdateLink(ctx, req, padmark.UpdateLinkParams{ID: input.ID})

	link, err := fetch[*padmark.LinkResponse](res, err, "update link", operation{
		context: "decode update link response", rateScope: "link updates", kind: linkRecord(),
	})
	if err != nil {
		return nil, linkOutput{}, err
	}

	return nil, linkOutputFromLink(link), nil
}

// newUpdateLinkRequest converts the tool input into the PUT body without the edit code. It
// refuses input that the model can correct before a server call.
func newUpdateLinkRequest(input updateLinkInput) (*padmark.UpdateLinkRequest, error) {
	parsed, err := parseTargetURL(input.TargetURL)
	if err != nil {
		return nil, err
	}

	err = checkTTL(input.TTL, 0, errLinkTTLRange)
	if err != nil {
		return nil, err
	}

	req := &padmark.UpdateLinkRequest{
		TargetURL:      *parsed,
		EditCode:       padmark.OptString{Value: "", Set: false},
		TTL:            padmark.OptNilInt64{Value: 0, Set: false, Null: false},
		Privacy:        padmark.OptUpdateLinkRequestPrivacy{Value: "", Set: false},
		DirectRedirect: padmark.OptNilBool{Value: false, Set: false, Null: false},
	}

	if input.TTL != nil {
		req.TTL = padmark.NewOptNilInt64(*input.TTL)
	}

	if input.Privacy != nil {
		req.Privacy = padmark.NewOptUpdateLinkRequestPrivacy(padmark.UpdateLinkRequestPrivacy(*input.Privacy))
	}

	if input.DirectRedirect != nil {
		req.DirectRedirect = padmark.NewOptNilBool(*input.DirectRedirect)
	}

	return req, nil
}

func (s *Server) deleteLink(
	ctx context.Context, _ *mcpsdk.CallToolRequest, input deleteLinkInput,
) (*mcpsdk.CallToolResult, deletedOutput, error) {
	params := padmark.DeleteLinkParams{
		ID:        input.ID,
		XEditCode: padmark.OptString{Value: "", Set: false},
	}
	if code := editCodeFor(s.vault, input.ID, input.EditCode); code != "" {
		// The header, not the query parameter — query strings land in access logs.
		params.XEditCode = padmark.NewOptString(code)
	}

	res, err := s.client.DeleteLink(ctx, params)

	_, err = fetch[*padmark.DeleteLinkNoContent](res, err, "delete link", operation{
		context: "delete link response", rateScope: "link deletes", kind: linkRecord(),
	})
	if err != nil {
		return nil, deletedOutput{}, err
	}

	return nil, deletedOutput{ID: input.ID, Deleted: true}, nil
}
