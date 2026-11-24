// Package source holds helpers shared by the photo sources.
package source

import (
	"html"
	"regexp"
	"strings"
	"time"
)

var tagRe = regexp.MustCompile(`<[^>]*>`)

// StripHTML removes tags and decodes entities, collapsing whitespace.
func StripHTML(s string) string {
	s = html.UnescapeString(tagRe.ReplaceAllString(s, " "))
	return strings.Join(strings.Fields(s), " ")
}

var (
	dateTimeRe = regexp.MustCompile(`(\d{4})[-:/](\d{2})[-:/](\d{2})[ T](\d{1,2}):(\d{2})(?::(\d{2}))?`)
	dateRe     = regexp.MustCompile(`(\d{4})-(\d{2})-(\d{2})`)
	longDateRe = regexp.MustCompile(`(\d{1,2}) (January|February|March|April|May|June|July|August|September|October|November|December) (\d{4})`)
)

// ParseTaken extracts a camera timestamp from free-form text such as EXIF
// "2011:09:12 18:42:03", "2011-09-12", or "Taken on 12 September 2011".
// hasTime reports whether a time of day was present. Implausible dates
// (before 1990 or in the future) are rejected: they are usually unset clocks.
func ParseTaken(s string) (t time.Time, hasTime, ok bool) {
	s = StripHTML(s)
	if m := dateTimeRe.FindString(s); m != "" {
		norm := strings.NewReplacer(":", "-", "/", "-").Replace(m[:10]) + m[10:]
		norm = strings.Replace(norm, "T", " ", 1)
		for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02 3:04"} {
			if t, err := time.Parse(layout, norm); err == nil {
				return t, true, plausible(t)
			}
		}
	}
	if m := dateRe.FindString(s); m != "" {
		if t, err := time.Parse("2006-01-02", m); err == nil {
			return t, false, plausible(t)
		}
	}
	if m := longDateRe.FindString(s); m != "" {
		if t, err := time.Parse("2 January 2006", m); err == nil {
			return t, false, plausible(t)
		}
	}
	return time.Time{}, false, false
}

func plausible(t time.Time) bool {
	return t.Year() >= 1990 && t.Before(time.Now().Add(48*time.Hour))
}
