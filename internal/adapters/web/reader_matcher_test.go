package web_test

import (
	"fmt"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// readerMatcher is a gomock argument matcher for the domain.Reader that a handler gives to a
// usecase. The tests check the caller fields that the request sets, and nothing more.
type readerMatcher struct {
	match func(reader domain.Reader) bool
	text  string
}

// Matches reports whether x is a domain.Reader that the matcher accepts.
func (m readerMatcher) Matches(x any) bool {
	reader, ok := x.(domain.Reader)

	return ok && m.match(reader)
}

// String describes the matcher in a gomock failure message.
func (m readerMatcher) String() string { return m.text }

// withBurn matches a domain.Reader with the burn intent allowBurn.
func withBurn(allowBurn bool) readerMatcher {
	return readerMatcher{
		match: func(reader domain.Reader) bool { return reader.AllowBurn == allowBurn },
		text:  "reader with AllowBurn " + boolText(allowBurn),
	}
}

// anonymousReader matches the reader of an anonymous request on a server with accounts.
func anonymousReader() readerMatcher {
	return readerMatcher{
		match: func(reader domain.Reader) bool {
			return reader.CallerID == uuid.Nil && !reader.Authenticated && !reader.SkipPrivacy
		},
		text: "anonymous reader with privacy on",
	}
}

// signedInReader matches the reader of the account userID on a server with accounts.
func signedInReader(userID uuid.UUID) readerMatcher {
	return readerMatcher{
		match: func(reader domain.Reader) bool {
			return reader.CallerID == userID && reader.Authenticated && !reader.SkipPrivacy
		},
		text: "signed-in reader " + userID.String() + " with privacy on",
	}
}

// openReader matches the reader of a server without accounts: no privacy level applies.
func openReader() readerMatcher {
	return readerMatcher{
		match: func(reader domain.Reader) bool { return reader.SkipPrivacy },
		text:  "reader with privacy off",
	}
}

// noCaller matches the reader of a request without a session or an API token, with or without
// the account system.
func noCaller() readerMatcher {
	return readerMatcher{
		match: func(reader domain.Reader) bool { return reader.CallerID == uuid.Nil && !reader.Authenticated },
		text:  "reader without a caller",
	}
}

// caller matches the reader of the account userID.
func caller(userID uuid.UUID) readerMatcher {
	return readerMatcher{
		match: func(reader domain.Reader) bool { return reader.CallerID == userID },
		text:  "reader " + userID.String(),
	}
}

// boolText returns "true" or "false" for the matcher descriptions.
func boolText(value bool) string {
	if value {
		return "true"
	}

	return "false"
}

// expectNoteReadError makes the note read of reader for id fail with err. The handler uses Read
// or ReadRendered by the format of the request, so the test permits each of them once.
func expectNoteReadError(manager *MockNoteManager, id string, reader gomock.Matcher, err error) {
	manager.EXPECT().Read(gomock.Any(), id, reader).Return(nil, err).MaxTimes(1)
	manager.EXPECT().ReadRendered(gomock.Any(), id, reader).Return(nil, "", err).MaxTimes(1)
}

// expectOtherKind makes the note read of GET /{id} tell that a record of kind has the slug, as
// notes.Storage.Get does in the same query. The handler then calls only the Manager of that kind.
func expectOtherKind(manager *MockNoteManager, id string, kind domain.RecordKind) {
	expectNoteReadError(manager, id, gomock.Any(), otherKindError(kind))
}

// expectOtherKindEditor is expectOtherKind for the note read of GET /edit/{id}, which is Inspect.
func expectOtherKindEditor(manager *MockNoteManager, id string, kind domain.RecordKind) {
	manager.EXPECT().Inspect(gomock.Any(), id, gomock.Any()).Return(nil, otherKindError(kind))
}

// otherKindError is the error of a note read for a slug of a record of kind, as notes.Manager
// wraps it.
func otherKindError(kind domain.RecordKind) error {
	return fmt.Errorf("inspect note: %w", &domain.OtherKindError{Kind: kind})
}
