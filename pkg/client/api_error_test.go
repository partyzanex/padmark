package client

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errorVariantNames returns the error variants that oas_schemas_gen.go declares: each defined
// type of ErrorResponse and each empty struct, other than the success variants without a body.
func errorVariantNames(t *testing.T) []string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), "oas_schemas_gen.go", nil, parser.SkipObjectResolution)
	require.NoError(t, err)

	var names []string

	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}

		for _, spec := range gen.Specs {
			typeSpec, isType := spec.(*ast.TypeSpec)
			if !isType || !isErrorVariant(typeSpec) {
				continue
			}

			names = append(names, typeSpec.Name.Name)
		}
	}

	return names
}

func isErrorVariant(spec *ast.TypeSpec) bool {
	name := spec.Name.Name
	if strings.HasSuffix(name, "NoContent") || strings.HasSuffix(name, "OK") {
		return false
	}

	switch typ := spec.Type.(type) {
	case *ast.Ident:
		return typ.Name == "ErrorResponse"
	case *ast.StructType:
		return len(typ.Fields.List) == 0
	default:
		return false
	}
}

func TestResponseStatusCoversVariants(t *testing.T) {
	t.Parallel()

	byName := make(map[string]int, len(responseStatus))
	for typ, status := range responseStatus {
		byName[typ.Name()] = status
	}

	names := errorVariantNames(t)
	require.NotEmpty(t, names)

	for _, name := range names {
		status, ok := byName[name]
		if !assert.True(t, ok, "error variant %s is not in responseStatus", name) {
			continue
		}

		// The generated name ends with the status text without spaces, for example
		// CreateNoteRequestEntityTooLarge for 413.
		suffix := strings.ReplaceAll(http.StatusText(status), " ", "")
		assert.True(t, strings.HasSuffix(name, suffix), "%s has status %d (%s)", name, status, suffix)
	}

	// The table has one more entry: ErrorResponse itself, the error body of Readyz.
	assert.Len(t, responseStatus, len(names)+1)
}

func TestResponseError_VariantWithBody(t *testing.T) {
	t.Parallel()

	apiErr, ok := ResponseError(&CreateNoteConflict{Message: "slug taken", Code: NewOptString("slug_conflict")})
	require.True(t, ok)
	assert.Equal(t, &APIError{Message: "slug taken", Code: "slug_conflict", StatusCode: http.StatusConflict}, apiErr)
}

func TestResponseError_VariantWithoutBody(t *testing.T) {
	t.Parallel()

	apiErr, ok := ResponseError(&UpdateNoteUnauthorized{})
	require.True(t, ok)
	assert.Equal(t, &APIError{Message: "", Code: "", StatusCode: http.StatusUnauthorized}, apiErr)
}

func TestResponseError_NilVariantPointer(t *testing.T) {
	t.Parallel()

	apiErr, ok := ResponseError((*GetNoteGone)(nil))
	require.True(t, ok, "the type of a nil pointer still gives the status")
	assert.Equal(t, &APIError{Message: "", Code: "", StatusCode: http.StatusGone}, apiErr)
}

func TestResponseError_ReadyzBody(t *testing.T) {
	t.Parallel()

	var res ReadyzRes = &ErrorResponse{Message: "db down", Code: OptString{Value: "", Set: false}}

	apiErr, ok := ResponseError(res)
	require.True(t, ok)
	assert.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
	assert.Equal(t, "db down", apiErr.Message)
}

func TestResponseError_NotAnError(t *testing.T) {
	t.Parallel()

	for _, res := range []any{nil, &NoteResponse{}, &DeleteNoteNoContent{}, CreateNoteConflict{}, (*NoteResponse)(nil), "x"} {
		apiErr, ok := ResponseError(res)
		assert.False(t, ok, "%T", res)
		assert.Nil(t, apiErr)
	}
}

func TestMaxTTLSecondsFitsDuration(t *testing.T) {
	t.Parallel()

	assert.Equal(t, int64(9223372036), MaxTTLSeconds, "openapi.yaml gives this maximum for each ttl field")
	assert.Equal(t, reflect.Int64, reflect.TypeOf(MaxTTLSeconds).Kind())
}
