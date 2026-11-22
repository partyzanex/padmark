//go:build integration

package mcpintegration

import (
	"context"
	"encoding/json"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// linkTargets returns targets that differ only in "#" and "%23". A target with a "#fragment"
// and a target with an encoded "%23" must keep the address that the agent sent.
func linkTargets() []string {
	return []string{
		"https://example.org/guide#install",
		"https://example.org/guide%23install",
		"https://example.org/guide?lang=en#install",
		"https://example.org/a%23b#c",
	}
}

// TestMCPFlow_LinkTargetKeepsItsAddress drives create_link and get_link against the real server
// stack. Both tools return the address that the agent sent.
func TestMCPFlow_LinkTargetKeepsItsAddress(t *testing.T) {
	ctx := context.Background()

	stack := startTestPadmark(t)

	sess := connectMCP(ctx, t, stack.srv.URL, "")
	defer sess.Close()

	for _, target := range linkTargets() {
		created := callOutput(ctx, t, sess, "create_link", map[string]any{"target_url": target})
		require.False(t, created.IsError, toolText(created))
		require.Equal(t, target, stringField(created, "target_url"), "create_link")

		read := callOutput(ctx, t, sess, "get_link", map[string]any{"id": resultID(created)})
		require.False(t, read.IsError, toolText(read))
		require.Equal(t, target, stringField(read, "target_url"), "get_link")
	}
}

// TestMCPFlow_BulkLinkTargetIsSavedExactly checks the address that create_links_bulk saves: the
// server stores the target of each item as the agent sent it. get_link reads the saved value.
func TestMCPFlow_BulkLinkTargetIsSavedExactly(t *testing.T) {
	ctx := context.Background()

	stack := startTestPadmark(t)

	sess := connectMCP(ctx, t, stack.srv.URL, "")
	defer sess.Close()

	targets := linkTargets()
	results := bulkResults(ctx, t, sess, targets)

	for idx, target := range targets {
		require.Empty(t, results[idx].Error, target)

		read := callOutput(ctx, t, sess, "get_link", map[string]any{"id": results[idx].ID})
		require.False(t, read.IsError, toolText(read))
		require.Equal(t, target, stringField(read, "target_url"), "saved target of bulk item %d", idx)
	}
}

// TestMCPFlow_BulkLinkEchoKeepsItsAddress checks the target_url of each create_links_bulk result.
// The contract of BulkLinkResult is that it echoes the target of the request item.
func TestMCPFlow_BulkLinkEchoKeepsItsAddress(t *testing.T) {
	ctx := context.Background()

	stack := startTestPadmark(t)

	sess := connectMCP(ctx, t, stack.srv.URL, "")
	defer sess.Close()

	targets := linkTargets()
	results := bulkResults(ctx, t, sess, targets)

	for idx, target := range targets {
		require.Equal(t, target, results[idx].TargetURL, "echoed target of bulk item %d", idx)
	}
}

type bulkResult struct {
	ID        string `json:"id"`
	TargetURL string `json:"target_url"`
	Error     string `json:"error"`
}

// bulkResults calls create_links_bulk and returns one result for each target, in order.
func bulkResults(ctx context.Context, t *testing.T, sess *mcpsdk.ClientSession, targets []string) []bulkResult {
	t.Helper()

	bulk := callOutput(ctx, t, sess, "create_links_bulk", map[string]any{"target_urls": targets})
	require.False(t, bulk.IsError, toolText(bulk))

	var out struct {
		Results []bulkResult `json:"results"`
	}

	require.NoError(t, json.Unmarshal([]byte(toolText(bulk)), &out))
	require.Len(t, out.Results, len(targets))

	return out.Results
}
