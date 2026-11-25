package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/files"
)

func updateRequest(body, editCode string) *http.Request {
	req := httptest.NewRequest(http.MethodPut, "/files/abc", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	if editCode != "" {
		req.Header.Set("X-Edit-Code", editCode)
	}

	return req
}

func (s *FileHandlerSuite) TestUpdate_WithEditCode() {
	updated := fileRecord("a.txt", 3, 2, true)
	authenticated := domain.PrivacyAuthenticated
	updated.Privacy = &authenticated

	s.files.EXPECT().Update(gomock.Any(), "abc", "code-1234", access(false), gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, _ domain.Reader, opts files.UpdateOptions) (*domain.File, error) {
			s.Require().NotNil(opts.ExpiresAt)
			s.WithinDuration(time.Now().Add(time.Hour), *opts.ExpiresAt, time.Minute)
			s.False(opts.ClearExpiry)
			s.Nil(opts.Privacy, "privacy is not public without accounts, so the test omits it")
			s.Require().NotNil(opts.BurnAfterDownload)
			s.True(*opts.BurnAfterDownload)

			return updated, nil
		})

	rec := s.serve(updateRequest(`{"ttl":3600,"burn_after_download":true}`, "code-1234"))
	s.Require().Equal(http.StatusOK, rec.Code)

	var body map[string]any
	s.Require().NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	s.Equal("a.txt", body["name"])
	s.Equal("authenticated", body["privacy"])
	s.Equal(true, body["burn_after_download"])
	s.NotContains(body, "edit_code")
}

func (s *FileHandlerSuite) TestUpdate_NullTTLClearsExpiry() {
	s.files.EXPECT().Update(gomock.Any(), "abc", "code-1234", access(false), files.UpdateOptions{
		ExpiresAt: nil, Privacy: nil, BurnAfterDownload: nil, ClearExpiry: true,
	}).Return(fileRecord("a.txt", 3, 0, false), nil)

	s.Equal(http.StatusOK, s.serve(updateRequest(`{"ttl":null}`, "code-1234")).Code)
}

func (s *FileHandlerSuite) TestUpdate_OwnerWithoutEditCode() {
	owner := &domain.User{
		ID: uuid.New(), Username: "owner",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	ownerPrivacy := domain.PrivacyOwner
	stored := fileRecord("a.txt", 3, 0, false)
	stored.OwnerID, stored.Privacy = &owner.ID, &ownerPrivacy

	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "owner-key").Return(owner, "token-hash", nil)
	s.files.EXPECT().Update(gomock.Any(), "abc", "", signedInReader(owner.ID), gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, _ domain.Reader, opts files.UpdateOptions) (*domain.File, error) {
			s.Require().NotNil(opts.Privacy)
			s.Equal(domain.PrivacyOwner, *opts.Privacy)

			return stored, nil
		})

	req := updateRequest(`{"privacy":"owner"}`, "")
	req.Header.Set("Authorization", "Bearer owner-key")

	rec := httptest.NewRecorder()
	s.newAuthRouter(s.auth).ServeHTTP(rec, req)
	s.Equal(http.StatusOK, rec.Code)
}

func (s *FileHandlerSuite) TestUpdate_InvisibleFileIsUnauthorized() {
	ownerID := uuid.New()
	ownerPrivacy := domain.PrivacyOwner
	stored := fileRecord("a.txt", 3, 0, false)
	stored.OwnerID, stored.Privacy = &ownerID, &ownerPrivacy

	// The usecase finds that the caller cannot see the file; the handler answers 401.
	s.files.EXPECT().Update(gomock.Any(), "abc", "code-1234", anonymousReader(), gomock.Any()).
		Return(nil, domain.ErrForbidden)

	rec := httptest.NewRecorder()
	s.newAuthRouter(s.auth).ServeHTTP(rec, updateRequest(`{"ttl":60}`, "code-1234"))
	s.Equal(http.StatusUnauthorized, rec.Code)
}

func (s *FileHandlerSuite) TestUpdate_InvalidRequest() {
	for _, testCase := range []struct {
		name   string
		body   string
		status int
	}{
		{"malformed JSON", `{"ttl":`, http.StatusBadRequest},
		{"edit code in the body", `{"edit_code":"code-1234"}`, http.StatusBadRequest},
		{"negative ttl", `{"ttl":-1}`, http.StatusUnprocessableEntity},
		{"fractional ttl", `{"ttl":1.5}`, http.StatusUnprocessableEntity},
		{"string ttl", `{"ttl":"60"}`, http.StatusUnprocessableEntity},
		{"too large ttl", `{"ttl":9223372037}`, http.StatusUnprocessableEntity},
		{"unknown privacy", `{"privacy":"friends"}`, http.StatusUnprocessableEntity},
		{"privacy without accounts", `{"privacy":"authenticated"}`, http.StatusUnprocessableEntity},
	} {
		s.Run(testCase.name, func() {
			s.Equal(testCase.status, s.serve(updateRequest(testCase.body, "code-1234")).Code)
		})
	}
}

func (s *FileHandlerSuite) TestUpdate_UsecaseErrors() {
	for _, testCase := range []struct {
		err    error
		status int
	}{
		{domain.ErrInvalidEditCode, http.StatusForbidden},
		{domain.ErrNotFound, http.StatusNotFound},
		{domain.ErrOwnerPrivacyRequiresOwner, http.StatusUnprocessableEntity},
	} {
		s.Run(testCase.err.Error(), func() {
			s.files.EXPECT().Update(gomock.Any(), "abc", "code-1234", access(false), gomock.Any()).
				Return(nil, testCase.err)

			s.Equal(testCase.status, s.serve(updateRequest(`{}`, "code-1234")).Code)
		})
	}
}

func (s *FileHandlerSuite) TestUpdate_CrossOrigin() {
	req := updateRequest(`{"ttl":60}`, "code-1234")
	req.Header.Set("Origin", "https://evil.example")

	s.Equal(http.StatusForbidden, s.serve(req).Code)
}

func (s *FileHandlerSuite) TestUpdate_FailedCodesLockOut() {
	s.files.EXPECT().Update(gomock.Any(), "abc", "wrong", access(false), gomock.Any()).
		Return(nil, domain.ErrInvalidEditCode).Times(10)

	for range 10 {
		s.Equal(http.StatusForbidden, s.serve(updateRequest(`{}`, "wrong")).Code)
	}

	s.Equal(http.StatusTooManyRequests, s.serve(updateRequest(`{}`, "wrong")).Code)
}

// --- trailing JSON ---

// TestUpdate_TrailingData (R-49): data after the JSON value gives 400; white space is accepted.
func (s *FileHandlerSuite) TestUpdate_TrailingData() {
	for _, body := range []string{`{"ttl":60} {"privacy":"owner"}`, `{"ttl":60}x`, `{"ttl":60}]`} {
		s.Run(body, func() {
			s.Equal(http.StatusBadRequest, s.serve(updateRequest(body, "code-1234")).Code)
		})
	}

	s.Run("white space", func() {
		s.files.EXPECT().Update(gomock.Any(), "abc", "code-1234", access(false), gomock.Any()).
			Return(fileRecord("a.txt", 3, 0, false), nil)

		s.Equal(http.StatusOK, s.serve(updateRequest("{\"ttl\":60}\n \t", "code-1234")).Code)
	})
}
