package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRandomString_Length(t *testing.T) {
	const chars = "abc"

	for _, length := range []int{1, 5, 10, 32} {
		got, err := randomString(chars, length)
		require.NoError(t, err)
		assert.Len(t, got, length, "length=%d", length)
	}
}

func TestRandomString_OnlyUsesCharset(t *testing.T) {
	const chars = "xyz"

	got, err := randomString(chars, 100)
	require.NoError(t, err)

	for _, ch := range got {
		assert.Contains(t, chars, string(ch))
	}
}

func TestRandomString_Entropy(t *testing.T) {
	first, err := randomString(slugChars, slugLength)
	require.NoError(t, err)

	second, err := randomString(slugChars, slugLength)
	require.NoError(t, err)

	assert.NotEqual(t, first, second, "two calls should almost never produce the same string")
}

func TestNewSlug_LengthAndCharset(t *testing.T) {
	slug, err := newSlug()
	require.NoError(t, err)

	assert.Len(t, slug, slugLength, "generated slug must be slugLength chars")
	assert.Equal(t, 10, slugLength, "slug length is the configured short-URL length")

	for _, ch := range slug {
		assert.Contains(t, slugChars, string(ch), "slug must only use the slug alphabet")
	}
}

func TestValidCustomSlug(t *testing.T) {
	cases := []struct {
		slug string
		want bool
	}{
		// single segment (legacy shapes still valid)
		{"my-slug", true},
		{"a_b-C9", true},
		{"file.name.md", true},
		// path-like multi-segment
		{"project/GUIDE.md", true},
		{"protocol-v1/README.md", true},
		{"a/b/c/d.txt", true},
		{strings.Repeat("a", 100), true}, // at length limit
		{"a/b/c/d/e/f/g/h/i/j", true},    // at segment limit (10 segments)
		{"llms", true},                   // "llms.txt" is reserved by exact segment, not by prefix
		{"llms-notes", true},
		{"llms/tips.md", true},
		// rejected
		{"", false},
		{"bad slug!", false},              // space / punctuation
		{"admin/x", false},                // reserved first segment
		{"edit/note", false},              // reserved first segment
		{"notes/x", false},                // reserved first segment
		{"links/x", false},                // reserved first segment (links)
		{"r/x", false},                    // reserved first segment (short-link redirect prefix)
		{"llms.txt", false},               // reserved first segment (served file, agent docs)
		{"llms.txt/x", false},             // reserved first segment — exact match also covers subpaths
		{"../etc", false},                 // segment starts with dot (traversal-ish)
		{".hidden", false},                // dotfile
		{"a/.hidden", false},              // dotfile in a later segment
		{"a//b", false},                   // empty segment
		{"/leading", false},               // leading slash
		{"trailing/", false},              // trailing slash
		{strings.Repeat("a", 101), false}, // over length limit
		{"a/b/c/d/e/f/g/h/i/j/k", false},  // over segment limit (11 segments, well under 100 chars)
	}

	for _, tc := range cases {
		assert.Equal(t, tc.want, ValidCustomSlug(tc.slug), "slug %q", tc.slug)
	}
}

func TestResolveSlug_ReservedPrefix_ErrorNamesReservedPrefixes(t *testing.T) {
	_, err := ResolveSlug("admin/x", true)

	require.ErrorIs(t, err, ErrInvalidSlug)
	assert.Contains(t, err.Error(), `reserved route name "admin"`)
	assert.Contains(t, err.Error(), "notes", "the reserved-name list should be spelled out")
}

func TestResolveSlug_InvalidShape_GenericMessage(t *testing.T) {
	_, err := ResolveSlug("bad slug!", true)

	require.ErrorIs(t, err, ErrInvalidSlug)
	assert.Equal(t, ErrInvalidSlug.Error(), err.Error(),
		"a shape violation unrelated to reserved prefixes should keep the generic message")
}

func TestResolveSlug_EmptyCandidate_GeneratesRandomSlug(t *testing.T) {
	slug, err := ResolveSlug("", false)

	require.NoError(t, err, "an empty candidate must always generate a slug, regardless of allowCustom")
	assert.Len(t, slug, slugLength)
}

func TestResolveSlug_CustomDisabled_RejectsNonEmptyCandidate(t *testing.T) {
	_, err := ResolveSlug("my-custom-slug", false)

	require.ErrorIs(t, err, ErrCustomSlugDisabled)
}

func TestResolveSlug_CustomEnabled_ReturnsCandidateUnchanged(t *testing.T) {
	slug, err := ResolveSlug("my-custom-slug", true)

	require.NoError(t, err)
	assert.Equal(t, "my-custom-slug", slug)
}

func TestHasGeneratedEditCodeShape(t *testing.T) {
	code, err := NewEditCode()
	require.NoError(t, err)
	assert.True(t, HasGeneratedEditCodeShape(code))
	assert.True(t, HasGeneratedEditCodeShape("rbHkuXQXtZ1t"))

	for _, code := range []string{"", "hunter2", "rbHkuXQXtZ1", "rbHkuXQXtZ1t1", "abc-def_ghi!", "ПриветМир12"} {
		assert.False(t, HasGeneratedEditCodeShape(code), code)
	}
}

func TestNewEditCode_LengthAndCharset(t *testing.T) {
	code, err := NewEditCode()
	require.NoError(t, err)

	assert.Len(t, code, editCodeLength)

	for _, ch := range code {
		assert.Contains(t, editCodeChars, string(ch))
	}
}
