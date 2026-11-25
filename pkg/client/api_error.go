// Package client is the Go client of the padmark HTTP API. ogen generates the operations from
// openapi.yaml (the oas_*_gen.go files). The other files add what the generated code does not
// give: UploadFile and DownloadFile stream files, ResponseError and APIError translate error
// responses, and SplitFragment repairs link targets.
package client

import (
	"fmt"
	"maps"
	"net/http"
	"reflect"
)

// This file is not generated. Keep it out of the ogen output names (oas_*_gen.go): ogen --clean
// removes only those files.

// APIError is a response with an error status. UploadFile and DownloadFile return it as their
// error, and ResponseError makes it from an error variant of a generated operation. Thus
// padmark-cli and padmark-mcp translate all error responses from one type. Use errors.As to read
// it from an error.
type APIError struct {
	// Message is the server message. UploadFile and DownloadFile use the HTTP status text when
	// the body has no message. ResponseError keeps an empty message: the generated decoder does
	// not tell a missing message from an empty one, and a variant without a body has none.
	Message string
	// Code is the optional machine-readable code, for example "burn_read_not_explicit" or
	// "api_disabled". Compare Code, not Message.
	Code string
	// StatusCode is the HTTP status code of the response.
	StatusCode int
}

// Error returns the status code and the server message.
func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("padmark: status %d: %s (code %s)", e.StatusCode, e.Message, e.Code)
	}

	return fmt.Sprintf("padmark: status %d: %s", e.StatusCode, e.Message)
}

// ResponseError returns the error variant res of a generated operation, for example
// *CreateNoteConflict, as an *APIError. The type gives the status, so a nil pointer of a variant
// type gives an *APIError without a message. ok is false for a success variant, a nil interface
// and a type that is not a response of this package. The caller then reports the type as
// unexpected.
func ResponseError(res any) (apiErr *APIError, ok bool) {
	value := reflect.ValueOf(res)
	if value.Kind() != reflect.Pointer {
		return nil, false
	}

	status, known := responseStatus()[value.Type().Elem()]
	if !known {
		return nil, false
	}

	apiErr = &APIError{Message: "", Code: "", StatusCode: status}
	bodyType := reflect.TypeFor[ErrorResponse]()

	// Most variants are defined types of ErrorResponse. A variant without a body is struct{}.
	if elem := value.Elem(); !value.IsNil() && elem.Type().ConvertibleTo(bodyType) {
		body, isBody := reflect.TypeAssert[ErrorResponse](elem.Convert(bodyType))
		if isBody {
			apiErr.Message = body.Message
			apiErr.Code = body.Code.Or("")
		}
	}

	return apiErr, true
}

// responseStatus gives the HTTP status of each error variant of the generated operations. The
// generated decoder keeps the status only in the type name. TestResponseStatusCoversVariants
// fails when a new variant of oas_schemas_gen.go is not in the table. The table is a function,
// not a package variable, so no code can change it. ResponseError runs only for error
// responses, so a new map for each call costs little.
func responseStatus() map[reflect.Type]int {
	statuses := map[reflect.Type]int{
		// Readyz returns the shared body itself for its only error status.
		reflect.TypeFor[ErrorResponse](): http.StatusServiceUnavailable,
	}

	maps.Copy(statuses, accountStatuses())
	maps.Copy(statuses, fileStatuses())
	maps.Copy(statuses, linkStatuses())
	maps.Copy(statuses, noteStatuses())

	return statuses
}

// accountStatuses gives the status of each error variant of the API token and invite operations.
func accountStatuses() map[reflect.Type]int {
	return map[reflect.Type]int{
		reflect.TypeFor[CreateAPITokenForbidden]():           http.StatusForbidden,
		reflect.TypeFor[CreateAPITokenInternalServerError](): http.StatusInternalServerError,
		reflect.TypeFor[CreateAPITokenServiceUnavailable]():  http.StatusServiceUnavailable,
		reflect.TypeFor[CreateAPITokenTooManyRequests]():     http.StatusTooManyRequests,
		reflect.TypeFor[CreateAPITokenUnauthorized]():        http.StatusUnauthorized,
		reflect.TypeFor[CreateAPITokenUnprocessableEntity](): http.StatusUnprocessableEntity,
		reflect.TypeFor[CreateInviteForbidden]():             http.StatusForbidden,
		reflect.TypeFor[CreateInviteInternalServerError]():   http.StatusInternalServerError,
		reflect.TypeFor[CreateInviteServiceUnavailable]():    http.StatusServiceUnavailable,
		reflect.TypeFor[CreateInviteTooManyRequests]():       http.StatusTooManyRequests,
		reflect.TypeFor[CreateInviteUnauthorized]():          http.StatusUnauthorized,
	}
}

// fileStatuses gives the status of each error variant of the file operations.
func fileStatuses() map[reflect.Type]int {
	return map[reflect.Type]int{
		reflect.TypeFor[CreateFileBadRequest]():              http.StatusBadRequest,
		reflect.TypeFor[CreateFileConflict]():                http.StatusConflict,
		reflect.TypeFor[CreateFileForbidden]():               http.StatusForbidden,
		reflect.TypeFor[CreateFileInternalServerError]():     http.StatusInternalServerError,
		reflect.TypeFor[CreateFileRequestEntityTooLarge]():   http.StatusRequestEntityTooLarge,
		reflect.TypeFor[CreateFileServiceUnavailable]():      http.StatusServiceUnavailable,
		reflect.TypeFor[CreateFileTooManyRequests]():         http.StatusTooManyRequests,
		reflect.TypeFor[CreateFileUnauthorized]():            http.StatusUnauthorized,
		reflect.TypeFor[CreateFileUnprocessableEntity]():     http.StatusUnprocessableEntity,
		reflect.TypeFor[DeleteFileForbidden]():               http.StatusForbidden,
		reflect.TypeFor[DeleteFileInternalServerError]():     http.StatusInternalServerError,
		reflect.TypeFor[DeleteFileNotFound]():                http.StatusNotFound,
		reflect.TypeFor[DeleteFileServiceUnavailable]():      http.StatusServiceUnavailable,
		reflect.TypeFor[DeleteFileTooManyRequests]():         http.StatusTooManyRequests,
		reflect.TypeFor[DeleteFileUnauthorized]():            http.StatusUnauthorized,
		reflect.TypeFor[GetFileContentConflict]():            http.StatusConflict,
		reflect.TypeFor[GetFileContentGone]():                http.StatusGone,
		reflect.TypeFor[GetFileContentInternalServerError](): http.StatusInternalServerError,
		reflect.TypeFor[GetFileContentNotFound]():            http.StatusNotFound,
		reflect.TypeFor[GetFileContentServiceUnavailable]():  http.StatusServiceUnavailable,
		reflect.TypeFor[GetFileContentUnauthorized]():        http.StatusUnauthorized,
		reflect.TypeFor[GetFileGone]():                       http.StatusGone,
		reflect.TypeFor[GetFileInternalServerError]():        http.StatusInternalServerError,
		reflect.TypeFor[GetFileNotFound]():                   http.StatusNotFound,
		reflect.TypeFor[GetFileServiceUnavailable]():         http.StatusServiceUnavailable,
		reflect.TypeFor[GetFileUnauthorized]():               http.StatusUnauthorized,
		reflect.TypeFor[UpdateFileBadRequest]():              http.StatusBadRequest,
		reflect.TypeFor[UpdateFileForbidden]():               http.StatusForbidden,
		reflect.TypeFor[UpdateFileGone]():                    http.StatusGone,
		reflect.TypeFor[UpdateFileInternalServerError]():     http.StatusInternalServerError,
		reflect.TypeFor[UpdateFileNotFound]():                http.StatusNotFound,
		reflect.TypeFor[UpdateFileRequestEntityTooLarge]():   http.StatusRequestEntityTooLarge,
		reflect.TypeFor[UpdateFileServiceUnavailable]():      http.StatusServiceUnavailable,
		reflect.TypeFor[UpdateFileTooManyRequests]():         http.StatusTooManyRequests,
		reflect.TypeFor[UpdateFileUnauthorized]():            http.StatusUnauthorized,
		reflect.TypeFor[UpdateFileUnprocessableEntity]():     http.StatusUnprocessableEntity,
	}
}

// linkStatuses gives the status of each error variant of the link operations.
func linkStatuses() map[reflect.Type]int {
	return map[reflect.Type]int{
		reflect.TypeFor[CreateLinkBadRequest]():                 http.StatusBadRequest,
		reflect.TypeFor[CreateLinkConflict]():                   http.StatusConflict,
		reflect.TypeFor[CreateLinkInternalServerError]():        http.StatusInternalServerError,
		reflect.TypeFor[CreateLinkRequestEntityTooLarge]():      http.StatusRequestEntityTooLarge,
		reflect.TypeFor[CreateLinkServiceUnavailable]():         http.StatusServiceUnavailable,
		reflect.TypeFor[CreateLinkTooManyRequests]():            http.StatusTooManyRequests,
		reflect.TypeFor[CreateLinkUnauthorized]():               http.StatusUnauthorized,
		reflect.TypeFor[CreateLinkUnprocessableEntity]():        http.StatusUnprocessableEntity,
		reflect.TypeFor[CreateLinkUnsupportedMediaType]():       http.StatusUnsupportedMediaType,
		reflect.TypeFor[CreateLinksBulkBadRequest]():            http.StatusBadRequest,
		reflect.TypeFor[CreateLinksBulkInternalServerError]():   http.StatusInternalServerError,
		reflect.TypeFor[CreateLinksBulkRequestEntityTooLarge](): http.StatusRequestEntityTooLarge,
		reflect.TypeFor[CreateLinksBulkServiceUnavailable]():    http.StatusServiceUnavailable,
		reflect.TypeFor[CreateLinksBulkTooManyRequests]():       http.StatusTooManyRequests,
		reflect.TypeFor[CreateLinksBulkUnauthorized]():          http.StatusUnauthorized,
		reflect.TypeFor[CreateLinksBulkUnsupportedMediaType]():  http.StatusUnsupportedMediaType,
		reflect.TypeFor[DeleteLinkForbidden]():                  http.StatusForbidden,
		reflect.TypeFor[DeleteLinkInternalServerError]():        http.StatusInternalServerError,
		reflect.TypeFor[DeleteLinkNotFound]():                   http.StatusNotFound,
		reflect.TypeFor[DeleteLinkServiceUnavailable]():         http.StatusServiceUnavailable,
		reflect.TypeFor[DeleteLinkTooManyRequests]():            http.StatusTooManyRequests,
		reflect.TypeFor[DeleteLinkUnauthorized]():               http.StatusUnauthorized,
		reflect.TypeFor[GetLinkGone]():                          http.StatusGone,
		reflect.TypeFor[GetLinkInternalServerError]():           http.StatusInternalServerError,
		reflect.TypeFor[GetLinkNotFound]():                      http.StatusNotFound,
		reflect.TypeFor[GetLinkServiceUnavailable]():            http.StatusServiceUnavailable,
		reflect.TypeFor[GetLinkUnauthorized]():                  http.StatusUnauthorized,
		reflect.TypeFor[UpdateLinkBadRequest]():                 http.StatusBadRequest,
		reflect.TypeFor[UpdateLinkForbidden]():                  http.StatusForbidden,
		reflect.TypeFor[UpdateLinkGone]():                       http.StatusGone,
		reflect.TypeFor[UpdateLinkInternalServerError]():        http.StatusInternalServerError,
		reflect.TypeFor[UpdateLinkNotFound]():                   http.StatusNotFound,
		reflect.TypeFor[UpdateLinkRequestEntityTooLarge]():      http.StatusRequestEntityTooLarge,
		reflect.TypeFor[UpdateLinkServiceUnavailable]():         http.StatusServiceUnavailable,
		reflect.TypeFor[UpdateLinkTooManyRequests]():            http.StatusTooManyRequests,
		reflect.TypeFor[UpdateLinkUnauthorized]():               http.StatusUnauthorized,
		reflect.TypeFor[UpdateLinkUnprocessableEntity]():        http.StatusUnprocessableEntity,
		reflect.TypeFor[UpdateLinkUnsupportedMediaType]():       http.StatusUnsupportedMediaType,
	}
}

// noteStatuses gives the status of each error variant of the note operations.
func noteStatuses() map[reflect.Type]int {
	return map[reflect.Type]int{
		reflect.TypeFor[CreateNoteBadRequest]():            http.StatusBadRequest,
		reflect.TypeFor[CreateNoteConflict]():              http.StatusConflict,
		reflect.TypeFor[CreateNoteInternalServerError]():   http.StatusInternalServerError,
		reflect.TypeFor[CreateNoteRequestEntityTooLarge](): http.StatusRequestEntityTooLarge,
		reflect.TypeFor[CreateNoteServiceUnavailable]():    http.StatusServiceUnavailable,
		reflect.TypeFor[CreateNoteUnauthorized]():          http.StatusUnauthorized,
		reflect.TypeFor[CreateNoteUnprocessableEntity]():   http.StatusUnprocessableEntity,
		reflect.TypeFor[CreateNoteUnsupportedMediaType]():  http.StatusUnsupportedMediaType,
		reflect.TypeFor[DeleteNoteForbidden]():             http.StatusForbidden,
		reflect.TypeFor[DeleteNoteInternalServerError]():   http.StatusInternalServerError,
		reflect.TypeFor[DeleteNoteNotFound]():              http.StatusNotFound,
		reflect.TypeFor[DeleteNoteServiceUnavailable]():    http.StatusServiceUnavailable,
		reflect.TypeFor[DeleteNoteTooManyRequests]():       http.StatusTooManyRequests,
		reflect.TypeFor[DeleteNoteUnauthorized]():          http.StatusUnauthorized,
		reflect.TypeFor[GetNoteConflict]():                 http.StatusConflict,
		reflect.TypeFor[GetNoteGone]():                     http.StatusGone,
		reflect.TypeFor[GetNoteInternalServerError]():      http.StatusInternalServerError,
		reflect.TypeFor[GetNoteNotFound]():                 http.StatusNotFound,
		reflect.TypeFor[GetNoteServiceUnavailable]():       http.StatusServiceUnavailable,
		reflect.TypeFor[GetNoteUnauthorized]():             http.StatusUnauthorized,
		reflect.TypeFor[UpdateNoteBadRequest]():            http.StatusBadRequest,
		reflect.TypeFor[UpdateNoteForbidden]():             http.StatusForbidden,
		reflect.TypeFor[UpdateNoteGone]():                  http.StatusGone,
		reflect.TypeFor[UpdateNoteInternalServerError]():   http.StatusInternalServerError,
		reflect.TypeFor[UpdateNoteNotFound]():              http.StatusNotFound,
		reflect.TypeFor[UpdateNoteRequestEntityTooLarge](): http.StatusRequestEntityTooLarge,
		reflect.TypeFor[UpdateNoteServiceUnavailable]():    http.StatusServiceUnavailable,
		reflect.TypeFor[UpdateNoteTooManyRequests]():       http.StatusTooManyRequests,
		reflect.TypeFor[UpdateNoteUnauthorized]():          http.StatusUnauthorized,
		reflect.TypeFor[UpdateNoteUnprocessableEntity]():   http.StatusUnprocessableEntity,
		reflect.TypeFor[UpdateNoteUnsupportedMediaType]():  http.StatusUnsupportedMediaType,
	}
}
