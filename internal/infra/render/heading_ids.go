package render

import (
	"strconv"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/util"
)

// headingIDs is a parser.IDs that produces exactly the IDs goldmark's built-in generator does
// ("heading", "heading-1", "heading-2", ...) in amortized linear time. The built-in one probes
// "<base>-1", "<base>-2", ... from 1 for every duplicate, so N headings with the same text cost
// O(N²): 16 KiB of "#\n" took seconds to render. Here every base remembers the next suffix to
// try; IDs are never released, so every suffix below it is known to be taken.
//
// One instance serves one Convert call: IDs are unique per document.
type headingIDs struct {
	used map[string]struct{}
	next map[string]int
}

func newHeadingIDs() *headingIDs {
	return &headingIDs{used: map[string]struct{}{}, next: map[string]int{}}
}

// Generate returns a document-unique ID for value, deduplicating with a numeric suffix.
func (s *headingIDs) Generate(value []byte, kind ast.NodeKind) []byte {
	base := slugify(value, kind)

	if _, taken := s.used[base]; !taken {
		s.used[base] = struct{}{}

		return []byte(base)
	}

	for suffix := max(s.next[base], 1); ; suffix++ {
		candidate := base + "-" + strconv.Itoa(suffix)
		if _, taken := s.used[candidate]; taken {
			continue
		}

		s.used[candidate] = struct{}{}
		s.next[base] = suffix + 1

		return []byte(candidate)
	}
}

// Put reserves an explicitly set ID so generated ones never collide with it.
func (s *headingIDs) Put(value []byte) {
	s.used[string(value)] = struct{}{}
}

// slugify mirrors goldmark's ids.Generate base: ASCII alphanumerics lowercased, spaces, '-' and
// '_' turned into '-', everything else (including multi-byte runes) dropped.
func slugify(value []byte, kind ast.NodeKind) string {
	value = util.TrimRightSpace(util.TrimLeftSpace(value))

	result := make([]byte, 0, len(value))

	for idx := 0; idx < len(value); {
		char := value[idx]
		width := int(util.UTF8Len(char))

		idx += width
		if width != 1 {
			continue
		}

		switch {
		case util.IsAlphaNumeric(char):
			if 'A' <= char && char <= 'Z' {
				char += 'a' - 'A'
			}

			result = append(result, char)
		case util.IsSpace(char) || char == '-' || char == '_':
			result = append(result, '-')
		}
	}

	if len(result) > 0 {
		return string(result)
	}

	if kind == ast.KindHeading {
		return "heading"
	}

	return "id"
}
