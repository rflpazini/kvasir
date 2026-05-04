package model

import (
	"regexp"
	"strings"
)

// Quality is the normalized video quality bucket extracted from a torrent
// title. Only two real buckets matter for filtering today (4K and 1080p);
// everything else falls into Other.
type Quality string

const (
	Quality4K    Quality = "4K"
	Quality1080p Quality = "1080p"
	QualityOther Quality = "Other"
)

// Valid reports whether q is one of the recognized buckets.
func (q Quality) Valid() bool {
	switch q {
	case Quality4K, Quality1080p, QualityOther:
		return true
	}
	return false
}

// QualityFromString parses a user-supplied quality token (case- and
// whitespace-insensitive) into the canonical Quality. Only the public
// buckets resolve; "720p" or noise return ok=false.
func QualityFromString(s string) (Quality, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "4k":
		return Quality4K, true
	case "1080p":
		return Quality1080p, true
	case "other":
		return QualityOther, true
	}
	return "", false
}

// Word-bounded patterns. Order matters: 4K wins over 1080p when both appear.
// Word boundaries protect against substring matches like "x1080pluskick".
var (
	re4K    = regexp.MustCompile(`(?i)\b(2160p|4k|uhd)\b`)
	re1080p = regexp.MustCompile(`(?i)\b(1080p|fullhd|fhd)\b`)
)

// ParseQuality extracts the dominant quality bucket from a torrent title.
// Returns QualityOther if no recognized marker is present.
func ParseQuality(title string) Quality {
	if re4K.MatchString(title) {
		return Quality4K
	}
	if re1080p.MatchString(title) {
		return Quality1080p
	}
	return QualityOther
}

// FilterByQuality returns only the results whose Quality is in the allowed
// set. A nil or empty filter is a no-op (returns the input slice unchanged).
// Order is preserved.
func FilterByQuality(results []Result, allowed []Quality) []Result {
	if len(allowed) == 0 {
		return results
	}
	allow := make(map[Quality]struct{}, len(allowed))
	for _, q := range allowed {
		allow[q] = struct{}{}
	}
	out := make([]Result, 0, len(results))
	for _, r := range results {
		if _, ok := allow[r.Quality]; ok {
			out = append(out, r)
		}
	}
	return out
}
