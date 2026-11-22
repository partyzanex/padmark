package web

import (
	"net/http"
	"unicode/utf8"

	"github.com/partyzanex/padmark/internal/adapters/web/middleware"
	"github.com/partyzanex/padmark/internal/domain"
)

// Values of headerView.Kind: the page family that selects the header controls.
const (
	// headerKindNote marks the note editor: the "Note" tab is current.
	headerKindNote = "note"
	// headerKindLink marks the short-link editor: the "Short link" tab is current.
	headerKindLink = "link"
	// headerKindFile marks the upload page: the "File" tab is current.
	headerKindFile = "file"
	// headerKindLogin marks the sign-in page: the "Sign in" button is current.
	headerKindLogin = "login"
	// headerKindView marks reader pages (note view, success): a "New" button replaces the tabs.
	headerKindView = "view"
	// headerKindPlain marks other pages, the admin panel too: the create tabs with no current tab.
	headerKindPlain = ""
)

// headerView is the data of the shared "header" template. Every page view model carries one in
// its Header field.
type headerView struct {
	// Kind is one of the headerKind* values.
	Kind string
	// Username is the name of the signed-in user; empty when nobody is signed in.
	Username string
	// CSRFToken protects the sign-out form of the account block.
	CSRFToken string
	// Accounts is true when the account system is attached; the "Sign in" button needs it.
	Accounts bool
	IsAdmin  bool
	// FilesEnabled is true when object storage is configured. The "File" tab needs it: without
	// object storage there is no upload page to open.
	FilesEnabled bool
	// Record is the kind of record that a reader page shows. The "New" button of a reader page
	// opens the create page of the same kind, as for notes.
	Record recordKind
}

// recordKind is the kind of a record: a note, a short link or a file. A small integer keeps
// headerView small, because the templates and the methods copy it by value.
type recordKind uint8

// Values of recordKind. A note is the default, so the zero value keeps the note pages.
const (
	recordNote recordKind = iota
	recordLink
	recordFile
)

// NewURL returns the create page of the record kind for the "New" button.
func (v headerView) NewURL() string {
	switch v.Record {
	case recordLink:
		return "/links"
	case recordFile:
		return "/files"
	default:
		return "/"
	}
}

// NewLabel returns the accessible name of the "New" button.
func (v headerView) NewLabel() string {
	switch v.Record {
	case recordLink:
		return "New link"
	case recordFile:
		return "New file"
	default:
		return "New note"
	}
}

// SignedIn reports whether the header shows the account block.
func (v headerView) SignedIn() bool {
	return v.Username != ""
}

// Tabs reports whether the header shows the create tabs: "Note", "Short link" and, when files
// are on, "File". All pages other than the reader pages show them.
func (v headerView) Tabs() bool {
	return v.Kind != headerKindView
}

// Initial returns the first letter of the username for the avatar tile.
func (v headerView) Initial() string {
	return initial(v.Username)
}

// forRecord returns a copy of the header for a reader page of the record kind.
func (v headerView) forRecord(record recordKind) headerView {
	v.Record = record

	return v
}

// initial returns the first letter of name for an avatar tile; templates call it as "initial".
// It returns "" for an empty or malformed name.
func initial(name string) string {
	first, _ := utf8.DecodeRuneInString(name)
	if first == utf8.RuneError {
		return ""
	}

	return string(first)
}

// noHeader returns the zero header for view-model builders that have no request. The renderer
// replaces it with common.header before the template runs.
func noHeader() headerView {
	return headerView{
		Kind: headerKindPlain, Username: "", CSRFToken: "", Accounts: false, IsAdmin: false, FilesEnabled: false,
		Record: recordNote,
	}
}

// header assembles the header data of a page. kind is one of the headerKind* values.
func (c *common) header(r *http.Request, kind string) headerView {
	view := headerView{
		Kind:      kind,
		Username:  "",
		CSRFToken: middleware.CSRFFromContext(r.Context()),
		Accounts:  bool(c.accounts),
		IsAdmin:   false,
		Record:    recordNote,
		// The file tab follows the configuration, not the user.
		FilesEnabled: c.fileMgr != nil,
	}

	usr := c.currentUser(r)
	if usr != nil {
		view.Username = usr.Username
		view.IsAdmin = usr.IsAdmin
	}

	return view
}

// currentUser returns the signed-in user of the request. It returns nil when nobody is signed in
// or when accounts are off. A failed session lookup counts as signed out (fail-soft): the header
// then offers "Sign in" and the page still renders.
func (c *common) currentUser(r *http.Request) *domain.User {
	// The auth middleware stores the user for protected routes; public routes need the lookup.
	usr := middleware.UserFromContext(r)
	if usr != nil {
		return usr
	}

	sessID := middleware.ExtractSessionID(r)
	if sessID == "" {
		return nil
	}

	// Without accounts, sessionChecker is disabledAccounts: the lookup fails and nobody is signed in.
	usr, err := c.sessionChecker.GetSession(r.Context(), sessID)
	if err != nil {
		return nil
	}

	return usr
}
