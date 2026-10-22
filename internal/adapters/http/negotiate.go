package http

import (
	"math"
	"net/http"
	"strconv"
	"strings"
)

const (
	mimeJSON     = "application/json"
	mimeHTML     = "text/html"
	mimePlain    = "text/plain"
	mimeMarkdown = "text/markdown"

	// acceptMIMEParts is the maximum number of parts when splitting a media range on ";" — the
	// remainder (further ";"-separated parameters) is parsed separately, not dropped.
	acceptMIMEParts = 2
)

type format uint8

const (
	formatJSON format = iota
	formatHTML
	formatPlain
)

// formatMIMEs map each servable format to its exact media types. formatOrder fixes the last
// tie-breaker when several formats score identically — a bare */* or text/* expresses no
// discrimination among them, so the server applies its own preference. JSON comes first: that
// is the documented machine default ("missing Accept → application/json"), and a client that
// sends */* (curl, most HTTP libraries) must keep getting the machine-readable body; HTML and
// plain follow.
// Specificity ranks of an Accept media range: an exact type/subtype names its types most
// precisely, a partial wildcard less, */* least.
const (
	specMatchAll = 0
	specWildcard = 1
	specExact    = 2
)

// acceptEntry is one parsed media-range element of an Accept header.
type acceptEntry struct {
	mime  string  // lowercase "type/subtype"; a range may carry wildcards
	q     float64 // effective quality clamped to [0, 1]; a value that never parses counts as 0
	spec  uint8   // specExact = exact type/subtype, specWildcard = partial wildcard, specMatchAll = */*
	order int     // position in the header — the final tie-breaker
}

// negotiate selects the response format for a request.
//
// ?raw=1 still overrides everything. Otherwise the Accept header is ranked by RFC 9110's Accept
// semantics rather than by header position:
//
//   - The q parameter ranks candidates; q=0 means "unacceptable". A refusal outranks anything
//     less specific that would resurrect the type — "text/html;q=0, */*" must not serve HTML.
//   - Specificity only breaks q ties: an exact type/subtype beats a partial wildcard
//     (text/*, application/*), which in turn beats */*.
//   - Header position breaks the remaining ties; formats still scoring identically resolve in
//     the formatOrder server preference (JSON, HTML, plain).
//
// A header that matches nothing padmark serves falls back to application/json — the same
// default as a missing Accept header, so unrecognised clients keep the machine-readable body.
func negotiate(r *http.Request) format {
	if r.URL.Query().Get("raw") == "1" {
		return formatPlain
	}

	accept := r.Header.Get("Accept")
	if accept == "" {
		return formatJSON
	}

	entries := parseAccept(accept)
	if len(entries) == 0 {
		return formatJSON
	}

	return selectFormat(entries)
}

// parseAccept extracts one acceptEntry per comma-separated media range. Unparseable garbage is
// skipped rather than failing the request: Accept is advisory.
func parseAccept(accept string) []acceptEntry {
	var entries []acceptEntry

	for part := range strings.SplitSeq(accept, ",") {
		fields := strings.SplitN(strings.TrimSpace(part), ";", acceptMIMEParts)
		if fields[0] == "" {
			continue
		}

		mime := strings.ToLower(strings.TrimSpace(fields[0]))
		if mime == "" {
			continue
		}

		params := ""
		if len(fields) > 1 {
			params = fields[1]
		}

		entries = append(entries, acceptEntry{
			mime:  mime,
			q:     acceptQuality(params),
			spec:  acceptSpecificity(mime),
			order: len(entries),
		})
	}

	return entries
}

// acceptQuality parses the ";"-separated parameters of one media range and returns the q value.
// An absent q means 1 (the RFC default); a malformed or negative value degrades to 0 — an
// entry the server cannot understand must never silently win the negotiation.
func acceptQuality(params string) float64 {
	for param := range strings.SplitSeq(params, ";") {
		name, value, found := strings.Cut(strings.TrimSpace(param), "=")
		if !found || strings.ToLower(strings.TrimSpace(name)) != "q" {
			continue
		}

		q, parseErr := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if parseErr != nil || q < 0 {
			return 0
		}

		return math.Min(q, 1)
	}

	return 1
}

// acceptSpecificity ranks how precisely a media range names its types: */* least, a partial
// wildcard (text/*, application/*, even a stray */subtype) in between, an exact pair most.
func acceptSpecificity(mime string) uint8 {
	typ, sub, _ := strings.Cut(mime, "/")

	switch {
	case typ == "*" && sub == "*":
		return specMatchAll
	case typ == "*" || sub == "*":
		return specWildcard
	default:
		return specExact
	}
}

// coversAny reports whether the media range accepts any of the format's exact MIME types.
func (e acceptEntry) coversAny(mimes []string) bool {
	rngType, rngSub, _ := strings.Cut(e.mime, "/")

	for _, mime := range mimes {
		typ, sub, _ := strings.Cut(mime, "/")

		switch {
		case e.mime == mime:
			return true
		case rngSub == "*" && (rngType == "*" || rngType == typ):
			return true
		case rngType == "*" && rngSub == sub:
			return true
		}
	}

	return false
}

// selectFormat returns the format the entries prefer.
//
// Each servable format is scored by its best matching entry — highest q, then highest
// specificity, then earliest position; the chosen entry also inherits the format-level
// formatOrder tie-break. A format is refused outright when a q=0 entry refuses it more
// precisely than any acceptable entry accepts it (the exact/wildcard veto below), so an
// explicit "unacceptable" always wins over a vague */* that would cover it.
func selectFormat(entries []acceptEntry) format {
	// The tables are locals rather than package globals: gochecknoglobals guards every var
	// here, and the negotiation runs once per request — the allocation is trivial.
	formatOrder := []format{formatJSON, formatHTML, formatPlain}
	formatMIMEs := map[format][]string{
		formatHTML:  {mimeHTML},
		formatPlain: {mimePlain, mimeMarkdown},
		formatJSON:  {mimeJSON},
	}

	type score struct {
		q      float64
		order  int
		format format
		spec   uint8
	}

	// Sentinel: formatCandidate only returns entries with q > 0, so any candidate always
	// outranks q: -1, and the zero order/spec of the sentinel never win a tie-break.
	best := score{format: formatJSON, q: -1, order: 0, spec: 0}

	for _, fmtName := range formatOrder {
		entry, ok := formatCandidate(entries, formatMIMEs[fmtName])
		if !ok {
			continue
		}

		current := score{q: entry.q, order: entry.order, format: fmtName, spec: entry.spec}

		switch {
		case current.q > best.q:
			best = current
		case current.q == best.q && current.spec > best.spec:
			best = current
		case current.q == best.q && current.spec == best.spec && current.order < best.order:
			best = current
		}
	}

	if best.q < 0 {
		return formatJSON
	}

	return best.format
}

// formatCandidate picks the acceptable entry serving one format, or reports that the format is
// refused: either nothing acceptable covers it, or a q=0 entry refuses it more precisely than
// any acceptable entry accepts it — so "text/html;q=0, */*" never serves HTML.
func formatCandidate(entries []acceptEntry, mimes []string) (acceptEntry, bool) {
	var (
		best        acceptEntry
		found       bool
		maxPositive = -1
		maxZero     = -1
	)

	for i := range entries {
		// Indexed rather than ranged by value: acceptEntry is wide enough that gocritic's
		// rangeValCopy flags copying it per iteration.
		entry := entries[i]
		if !entry.coversAny(mimes) {
			continue
		}

		if entry.q > 0 {
			if int(entry.spec) > maxPositive {
				maxPositive = int(entry.spec)
			}

			// Strict comparisons only: for equal (q, spec) the earlier header position must
			// win, and entries are visited in ascending order — so the first one stays.
			if !found || entry.q > best.q || (entry.q == best.q && entry.spec > best.spec) {
				best, found = entry, true
			}

			continue
		}

		if int(entry.spec) > maxZero {
			maxZero = int(entry.spec)
		}
	}

	if !found || maxZero > maxPositive {
		return best, false
	}

	return best, true
}
