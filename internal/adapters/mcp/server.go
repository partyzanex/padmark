package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// version is reported in the MCP initialize result. A non-empty version is required by the
// SDK; when APP_VERSION is absent (go run / test builds), "dev" is used.
func version() string {
	if build := os.Getenv("APP_VERSION"); build != "" {
		return build
	}

	return "dev"
}

// Manager holds the shared state of the stdio MCP server: the padmark client every tool call
// goes through and the session's edit-code vault. The tool surface and its transport are owned
// by the MCP SDK; this type is only the composition and shared state.
type Manager struct {
	client PadmarkClient
	vault  *editCodeVault
	log    *slog.Logger
	srv    *mcpsdk.Server
}

// NewManager builds an MCP server exposing padmark's REST surface as tools, targeting the
// padmark instance the client is pointed at. No connection is opened here (no eager ping):
// server/network failures surface per tool call as structured results with remediation hints
// (fail-soft) — a stdio process that dies on a missing server just breaks the agent's whole
// tool set, and padmark may legitimately be started later.
func NewManager(client PadmarkClient, log *slog.Logger) *Manager {
	m := &Manager{
		client: client,
		vault:  newEditCodeVault(),
		log:    log,
		srv:    nil,
	}

	srv := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:        "padmark-mcp",
		Version:     version(),
		Title:       "",
		Description: "",
		WebsiteURL:  "",
		Icons:       nil,
	}, nil)
	m.registerTools(srv)
	m.srv = srv

	return m
}

// Run serves MCP over stdio until stdin closes, the context is cancelled, or the SDK reports a
// transport error. A client disconnecting (stdin EOF) is a session ending normally, not an error.
func (m *Manager) Run(ctx context.Context) error {
	return m.Serve(ctx, &mcpsdk.StdioTransport{MaxLineLength: 0})
}

// Serve serves MCP over an explicit transport — the seam for alternative transports (stdio is
// Run). Error semantics match Run.
func (m *Manager) Serve(ctx context.Context, transport mcpsdk.Transport) error {
	err := m.srv.Run(ctx, transport)
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, mcpsdk.ErrConnectionClosed) {
		// The normal way a session ends. A nil must short-circuit here: fmt.Errorf with %w on
		// nil still yields a non-nil error, which turned every clean shutdown into exit 1.
		return nil
	}

	return fmt.Errorf("mcp server run: %w", err)
}

// ConnectSession connects one raw MCP server session on the given transport — the seam tests
// use in place of Serve, exercising the same registered tool set without owning a process.
func (m *Manager) ConnectSession(ctx context.Context, transport mcpsdk.Transport) (*mcpsdk.ServerSession, error) {
	srvSession, err := m.srv.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect mcp session: %w", err)
	}

	return srvSession, nil
}

// registerTools wires every tool of the surface onto the SDK server.
func (m *Manager) registerTools(srv *mcpsdk.Server) {
	registerNoteTools(srv, m)
	registerLinkTools(srv, m)

	mcpsdk.AddTool[checkServerInput, checkServerOutput](srv, checkServerTool(), m.checkServer)
}
