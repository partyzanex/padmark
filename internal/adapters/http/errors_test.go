package http

import (
	"errors"
	"fmt"
	net_http "net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/partyzanex/padmark/internal/domain"
)

func TestDomainErrStatus(t *testing.T) {
	tests := []struct {
		err        error
		wantStatus int
		wantEmpty  bool
	}{
		{domain.ErrNotFound, net_http.StatusNotFound, false},
		{domain.ErrExpired, net_http.StatusGone, false},
		{domain.ErrTitleTooLong, net_http.StatusUnprocessableEntity, false},
		{domain.ErrInvalidContentType, net_http.StatusUnprocessableEntity, false},
		{domain.ErrInvalidSlug, net_http.StatusUnprocessableEntity, false},
		{domain.ErrCustomSlugDisabled, net_http.StatusUnprocessableEntity, false},
		{domain.ErrSlugConflict, net_http.StatusConflict, false},
		{domain.ErrInvalidEditCode, net_http.StatusForbidden, false},
		{domain.ErrForbidden, net_http.StatusForbidden, false},
		{errors.New("boom"), net_http.StatusInternalServerError, true},
		// A wrapped sentinel must still classify correctly (errors.Is, not direct equality).
		{fmt.Errorf("get note: %w", domain.ErrNotFound), net_http.StatusNotFound, false},
	}

	for _, testCase := range tests {
		status, msg := domainErrStatus(testCase.err)

		assert.Equal(t, testCase.wantStatus, status, "status for %v", testCase.err)

		if testCase.wantEmpty {
			assert.Empty(t, msg, "message for %v", testCase.err)
		} else {
			assert.NotEmpty(t, msg, "message for %v", testCase.err)
		}
	}
}

func TestErrorStatusMessage_UnknownError_GenericMessage(t *testing.T) {
	status, msg := errorStatusMessage(errors.New("boom"))

	assert.Equal(t, net_http.StatusInternalServerError, status)
	assert.Equal(t, "internal server error", msg)
}

func TestErrorStatusMessage_KnownError_UsesSentinelMessage(t *testing.T) {
	status, msg := errorStatusMessage(domain.ErrSlugConflict)

	assert.Equal(t, net_http.StatusConflict, status)
	assert.Equal(t, domain.ErrSlugConflict.Error(), msg)
}

// TestDomainErrToPageData_ValidationErrors_NotSilently500 covers errors that have no
// page-specific copy in domainErrToPageData (create/update validation errors, currently only
// ever produced on JSON-only endpoints) — they must still classify to their real 4xx status via
// domainErrToDefaultPageData instead of silently defaulting to 500, in case a future HTML path
// ever surfaces one of them.
func TestDomainErrToPageData_ValidationErrors_NotSilently500(t *testing.T) {
	tests := []struct {
		err        error
		wantStatus int
	}{
		{domain.ErrTitleTooLong, net_http.StatusUnprocessableEntity},
		{domain.ErrInvalidContentType, net_http.StatusUnprocessableEntity},
		{domain.ErrInvalidSlug, net_http.StatusUnprocessableEntity},
		{domain.ErrCustomSlugDisabled, net_http.StatusUnprocessableEntity},
		{domain.ErrSlugConflict, net_http.StatusConflict},
	}

	for _, tc := range tests {
		data := domainErrToPageData(tc.err)

		assert.Equal(t, tc.wantStatus, data.Code, "status for %v", tc.err)
		assert.Equal(t, errorTypeClient, data.ErrorType, "error type for %v", tc.err)
		assert.NotEmpty(t, data.Desc, "desc for %v", tc.err)
	}
}

func TestDomainErrToPageData_UnknownError_500(t *testing.T) {
	data := domainErrToPageData(errors.New("boom"))

	assert.Equal(t, net_http.StatusInternalServerError, data.Code)
	assert.Equal(t, errorTypeServer, data.ErrorType)
}

func TestDomainErrToPageData_NotFound_HasSpecificCopy(t *testing.T) {
	data := domainErrToPageData(domain.ErrNotFound)

	assert.Equal(t, net_http.StatusNotFound, data.Code)
	assert.Equal(t, "Paste not found", data.Title)
}
