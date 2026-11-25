package testenv

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/expiredslugs"
	"github.com/partyzanex/padmark/internal/infra/linkcache"
	"github.com/partyzanex/padmark/internal/infra/render"
	pgrepo "github.com/partyzanex/padmark/internal/infra/storage/postgres"
	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/testutil"
	"github.com/partyzanex/padmark/internal/usecases/auth"
	"github.com/partyzanex/padmark/internal/usecases/links"
	"github.com/partyzanex/padmark/internal/usecases/notes"
)

const (
	// CSRFKey is the CSRF key of a stack with accounts (WithAccounts).
	CSRFKey = "padmark-test-csrf-secret-32bytes"
	// maxBodyBytes is the body limit of the router of the test stacks.
	maxBodyBytes = 256 * 1024
	// totpIssuer is the TOTP issuer of the account system of the test stacks.
	totpIssuer = "padmark-test"
)

// Option changes NewStack or NewManagers.
type Option func(*options)

type options struct {
	wrapNotes   func(notes.Storage) notes.Storage
	wrapLinks   func(links.Storage) links.Storage
	configure   func(*web.HandlerConfig, *Stack)
	accounts    bool
	customSlugs bool
	linkCache   bool
}

// WithAccounts turns the account system on, with CSRFKey as the CSRF key. NewManagers
// ignores it.
func WithAccounts() Option { return func(o *options) { o.accounts = true } }

// WithCustomSlugs turns custom slugs on for notes and links.
func WithCustomSlugs() Option { return func(o *options) { o.customSlugs = true } }

// WithLinkCache gives the link manager the default link cache, as the server does.
func WithLinkCache() Option { return func(o *options) { o.linkCache = true } }

// WithNoteStorage changes the storage of the note manager, for example to pause a create. The
// expired-slug releaser keeps the repository.
func WithNoteStorage(wrap func(notes.Storage) notes.Storage) Option {
	return func(o *options) { o.wrapNotes = wrap }
}

// WithLinkStorage is WithNoteStorage for the link manager.
func WithLinkStorage(wrap func(links.Storage) links.Storage) Option {
	return func(o *options) { o.wrapLinks = wrap }
}

// WithConfig changes the handler configuration of a stack before NewStack builds the handler.
// configure gets the stack without its Server. NewManagers ignores it.
func WithConfig(configure func(cfg *web.HandlerConfig, stack *Stack)) Option {
	return func(o *options) { o.configure = configure }
}

func newOptions(opts []Option) *options {
	settings := &options{
		wrapNotes: func(storage notes.Storage) notes.Storage { return storage },
		wrapLinks: func(storage links.Storage) links.Storage { return storage },
		configure: func(*web.HandlerConfig, *Stack) {},
		accounts:  false, customSlugs: false, linkCache: false,
	}

	for _, opt := range opts {
		opt(settings)
	}

	return settings
}

// Managers holds the note and link managers of one database.
type Managers struct {
	Notes *notes.Manager
	Links *links.Manager
	// Expired is the expired-slug releaser that the managers share. A file manager joins it
	// through WithFiles.
	Expired *expiredslugs.Releaser
}

// NewManagers builds the note and link managers over the repositories of db with one shared
// expiredslugs.Releaser, as the composition root does. The dialect of db selects the SQLite or
// the PostgreSQL repositories.
func NewManagers(t *testing.T, db *bun.DB, opts ...Option) Managers {
	t.Helper()

	settings := newOptions(opts)
	log := slog.New(slog.DiscardHandler)

	var (
		noteRepo expiredslugs.RecordExpirer
		linkRepo expiredslugs.RecordExpirer
		noteDB   notes.Storage
		linkDB   links.Storage
	)

	if db.Dialect().Name() == dialect.PG {
		pgNotes, pgLinks := pgrepo.NewNoteRepository(db), pgrepo.NewLinkRepository(db, log)
		noteRepo, linkRepo, noteDB, linkDB = pgNotes, pgLinks, pgNotes, pgLinks
	} else {
		sqliteNotes, sqliteLinks := sqliterepo.NewNoteRepository(db), sqliterepo.NewLinkRepository(db, log)
		noteRepo, linkRepo, noteDB, linkDB = sqliteNotes, sqliteLinks, sqliteNotes, sqliteLinks
	}

	expired := expiredslugs.New(noteRepo, linkRepo)
	hasher := crypto.NewEditCodeHasher(testutil.Argon2Params(), log)

	noteManager := notes.NewManager(&notes.Deps{
		Storage: settings.wrapNotes(noteDB), Renderer: render.NewRenderer(),
		Encryptor: testutil.ContentEncryptor(t, "note"), Hasher: hasher, ExpiredSlugs: expired, Log: log,
		Now: time.Now,
	}, domain.DefaultMaxContentLength, settings.customSlugs)
	linkManager := links.NewManager(&links.Deps{
		Storage: settings.wrapLinks(linkDB), Encryptor: testutil.ContentEncryptor(t, "link"),
		Hasher: hasher, ExpiredSlugs: expired, Log: log, Now: time.Now,
	}, settings.customSlugs)

	if settings.linkCache {
		linkManager.WithCache(linkcache.NewDefault(log))
	}

	return Managers{Notes: noteManager, Links: linkManager, Expired: expired}
}

// SQLite opens the shared-cache in-memory SQLite database name and applies the migrations. Two
// calls with one name in one test process share the database. The test cleanup closes it.
func SQLite(t *testing.T, name string) *bun.DB {
	t.Helper()

	db, err := sqliterepo.Open("file:"+name+"?mode=memory&cache=shared", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = sqliterepo.Migrate(t.Context(), db)
	require.NoError(t, err)

	return db
}

// Accounts builds the account system over the SQLite repositories of db, as the composition
// root does, with the minimal argon2id cost (testutil.Argon2Params) and a session TTL of one
// hour. It also returns the user repository.
func Accounts(t *testing.T, db *bun.DB) (*auth.Manager, *sqliterepo.UserRepository) {
	t.Helper()

	log := slog.New(slog.DiscardHandler)
	users := sqliterepo.NewUserRepository(db)

	manager, err := auth.NewManager(
		auth.Stores{
			Users:     users,
			Invites:   sqliterepo.NewInviteRepository(db),
			Sessions:  sqliterepo.NewSessionRepository(db),
			APITokens: sqliterepo.NewAPITokenRepository(db),
		},
		&auth.Deps{
			PasswordHasher: crypto.NewPasswordHasher(testutil.Argon2Params(), log),
			Sealer:         crypto.NewSealer(testutil.Argon2Params()),
			TOTP:           crypto.NewTOTP(),
			Log:            log,
			Now:            time.Now,
		},
		totpIssuer, time.Hour,
	)
	require.NoError(t, err)

	return manager, users
}

// HandlerConfig returns the web.HandlerConfig of the test stacks: the given managers, no
// database pinger, a discarding logger, no account system, no files, no reveal store and the
// default settings. A test sets the fields of its scenario on the returned value.
func HandlerConfig(noteManager web.NoteManager, linkManager web.LinkManager) *web.HandlerConfig {
	return &web.HandlerConfig{
		Notes: noteManager, Links: linkManager, Pinger: web.NoPinger{}, Log: slog.New(slog.DiscardHandler),
		Accounts: nil, Files: nil, Reveal: nil,
		ForcedScheme: "", BootstrapToken: "", TrustedProxies: nil, CSRFSecret: nil, SessionTTL: 0,
		FileTransfer:   web.FileTransferLimits{IdleTimeout: 0, MaxUploads: 0, MaxUploadsPerClient: 0},
		DirectRedirect: false,
	}
}

// Router builds web.NewHandler and web.NewRouter from cfg with the body limit of the test stacks
// and no rate limit. It stops the test when a constructor fails.
func Router(t *testing.T, cfg *web.HandlerConfig) http.Handler {
	t.Helper()

	handler, err := web.NewHandler(cfg)
	require.NoError(t, err)

	router, err := web.NewRouter(handler, &web.RouterOptions{
		MaxBodyBytes: maxBodyBytes,

		AllowedHosts: nil, RateLimit: 0, RateBurst: 0, DisableAPI: false,
	})
	require.NoError(t, err)

	return router
}

// Serve serves Router(cfg) on an httptest server. The test cleanup closes the server.
func Serve(t *testing.T, cfg *web.HandlerConfig) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(Router(t, cfg))
	t.Cleanup(server.Close)

	return server
}

// Stack is the web stack of an integration test: one migrated in-memory SQLite database, the
// note and link managers, the account system when it is on, and the production router behind
// an httptest server.
type Stack struct {
	Managers

	DB *bun.DB
	// Auth is the account system, or nil without WithAccounts.
	Auth *auth.Manager
	// Users is the user repository, or nil without WithAccounts.
	Users *sqliterepo.UserRepository
	// Server serves the router.
	Server *httptest.Server
}

// NewStack builds the stack over the in-memory SQLite database name (see SQLite). The options
// select the differences between the tests.
func NewStack(t *testing.T, name string, opts ...Option) *Stack {
	t.Helper()

	settings := newOptions(opts)
	db := SQLite(t, name)
	stack := &Stack{Managers: NewManagers(t, db, opts...), DB: db, Auth: nil, Users: nil, Server: nil}
	cfg := HandlerConfig(stack.Notes, stack.Links)

	if settings.accounts {
		stack.Auth, stack.Users = Accounts(t, db)
		cfg.Accounts = stack.Auth
		cfg.CSRFSecret = []byte(CSRFKey)
	}

	settings.configure(cfg, stack)
	stack.Server = Serve(t, cfg)

	return stack
}
