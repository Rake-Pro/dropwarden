package update

import (
	"regexp"
	"strings"
	"time"
)

// ReleaseNotes is one published release's notes, as shown in the dashboard's
// "What's new" dialog before an update.
type ReleaseNotes struct {
	Version string
	Date    time.Time
	Body    string // release notes (markdown)
}

// MinorBump reports whether moving from current to next changes the major or
// minor version (1.2.9 -> 1.3.0 yes, 1.2.9 -> 1.2.10 no). Unparsable
// versions count as no bump.
func MinorBump(current, next string) bool {
	c, _, err := parseSemver(strings.TrimPrefix(current, "v"))
	if err != nil {
		return false
	}
	n, _, err := parseSemver(strings.TrimPrefix(next, "v"))
	if err != nil {
		return false
	}
	return c[0] != n[0] || c[1] != n[1]
}

// Notes returns the release's notes. The manifest carries only the latest
// release's body, so this is always a single entry (empty body when the
// manifest had none); versions skipped in between are not listed.
func (r *Release) Notes() []ReleaseNotes {
	if r == nil {
		return nil
	}
	return []ReleaseNotes{{Version: r.Version, Date: r.PublishedAt, Body: r.Body}}
}

var (
	mdLink   = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	mdInline = regexp.MustCompile("[*_`]{1,2}")
)

// NoteLine is one rendered line of release notes.
type NoteLine struct {
	Text    string `json:"text"`
	Heading bool   `json:"heading"`
}

// RenderNotes flattens markdown release bodies into plain lines: headings
// keep their text and are flagged, bullets get a "- " marker, links keep
// their label, emphasis markers are dropped, blank runs collapse. Each
// release is introduced by a "vX.Y.Z (date)" heading.
func RenderNotes(notes []ReleaseNotes) []NoteLine {
	var out []NoteLine
	for _, n := range notes {
		head := "v" + n.Version
		if !n.Date.IsZero() {
			head += "  (" + n.Date.Format("2006-01-02") + ")"
		}
		out = append(out, NoteLine{Text: head, Heading: true})
		blank := false
		for _, raw := range strings.Split(strings.ReplaceAll(n.Body, "\r\n", "\n"), "\n") {
			line := strings.TrimSpace(raw)
			if line == "" {
				blank = true
				continue
			}
			if blank && len(out) > 0 && !out[len(out)-1].Heading {
				out = append(out, NoteLine{Text: ""})
			}
			blank = false
			line = mdLink.ReplaceAllString(line, "$1")
			switch {
			case strings.HasPrefix(line, "#"):
				out = append(out, NoteLine{Text: strings.TrimSpace(strings.TrimLeft(line, "#")), Heading: true})
			case strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* "):
				out = append(out, NoteLine{Text: "- " + mdInline.ReplaceAllString(line[2:], "")})
			default:
				out = append(out, NoteLine{Text: mdInline.ReplaceAllString(line, "")})
			}
		}
		out = append(out, NoteLine{Text: ""})
	}
	return out
}
