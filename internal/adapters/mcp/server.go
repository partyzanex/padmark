package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/partyzanex/padmark/internal/errorcontext"
)

// version is reported in the MCP initialize result. A non-empty version is required by the
// SDK; when APP_VERSION is absent (go run / test builds), "dev" is used.
func version() string {
	if build := os.Getenv("APP_VERSION"); build != "" {
		return build
	}

	return "dev"
}

// Server is the stdio MCP server of padmark-mcp. It holds the tool handlers, the SDK server that
// they are registered on, the padmark client every tool call goes through and the session's
// edit-code vault. It is an adapter, not a business component: the MCP SDK owns the protocol
// and padmark owns the business rules.
type Server struct {
	client      PadmarkClient
	vault       *editCodeVault
	log         *slog.Logger
	srv         *mcpsdk.Server
	uploadRoot  string
	callTimeout time.Duration
	hasToken    bool
}

// Option sets an optional Server setting.
type Option func(*Server)

// WithUploadRoot lets upload_file read regular files under root, also through symbolic links
// that stay under root. Without this option, upload_file refuses every path.
func WithUploadRoot(root string) Option {
	return func(s *Server) {
		s.uploadRoot = root
	}
}

// WithCallTimeout sets the time limit of each tool call other than upload_file. The limit
// covers all requests of the call and the read of their response bodies. A zero timeout turns
// the limit off. Without this option, the limit is DefaultTimeout.
func WithCallTimeout(timeout time.Duration) Option {
	return func(s *Server) {
		s.callTimeout = timeout
	}
}

// WithAccountToken tells the server that the client sends a bearer token. upload_file then
// makes "owner" its default privacy: only the account of the token can read a file that the
// agent uploads without a privacy field.
func WithAccountToken() Option {
	return func(s *Server) {
		s.hasToken = true
	}
}

// NewServer builds an MCP server exposing padmark's REST surface as tools, targeting the
// padmark instance the client is pointed at. The proxy has one client for its one server.
// No connection is opened here (no eager ping):
// server/network failures surface per tool call as structured results with remediation hints
// (fail-soft) — a stdio process that dies on a missing server just breaks the agent's whole
// tool set, and padmark may legitimately be started later.
func NewServer(client PadmarkClient, log *slog.Logger, opts ...Option) *Server {
	proxy := &Server{
		client:      client,
		vault:       newEditCodeVault(),
		log:         log,
		srv:         nil,
		uploadRoot:  DefaultUploadRoot,
		callTimeout: DefaultTimeout,
		hasToken:    false,
	}

	for _, opt := range opts {
		opt(proxy)
	}

	srv := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:        "padmark-mcp",
		Version:     version(),
		Title:       "",
		Description: "",
		WebsiteURL:  "",
		Icons:       nil,
	}, nil)
	proxy.registerTools(srv)
	proxy.srv = srv

	return proxy
}

// Run serves MCP over stdio until stdin closes, the context is cancelled, or the SDK reports a
// transport error. A client disconnecting (stdin EOF) is a session ending normally, not an error.
func (s *Server) Run(ctx context.Context) error {
	return errorcontext.Wrap(s.Serve(ctx, &mcpsdk.StdioTransport{MaxLineLength: 0}), "serve")
}

// Serve serves MCP over an explicit transport — the seam for alternative transports (stdio is
// Run). Error semantics match Run.
func (s *Server) Serve(ctx context.Context, transport mcpsdk.Transport) error {
	err := s.srv.Run(ctx, transport)
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, mcpsdk.ErrConnectionClosed) {
		// The normal way a session ends. A nil must short-circuit here: fmt.Errorf with %w on
		// nil still yields a non-nil error, which turned every clean shutdown into exit 1.
		return nil
	}

	return fmt.Errorf("mcp server run: %w", err)
}

// ConnectSession connects one raw MCP server session on the given transport — the seam tests
// use in place of Serve, exercising the same registered tool set without owning a process.
func (s *Server) ConnectSession(ctx context.Context, transport mcpsdk.Transport) (*mcpsdk.ServerSession, error) {
	srvSession, err := s.srv.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect mcp session: %w", err)
	}

	return srvSession, nil
}

// registerTools wires every tool of the surface onto the SDK server.
func (s *Server) registerTools(srv *mcpsdk.Server) {
	registerNoteTools(srv, s)
	registerLinkTools(srv, s)
	registerFileTools(srv, s)

	mcpsdk.AddTool[checkServerInput, checkServerOutput](srv, checkServerTool(), limitCall(s, s.checkServer))
}
