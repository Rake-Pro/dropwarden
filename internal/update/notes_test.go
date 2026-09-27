package update

import (
	"strings"
	"testing"
	"time"
)

func TestMinorBump(t *testing.T) {
	cases := []struct {
		cur, next string
		want      bool
	}{
		{"1.2.9", "1.2.10", false},
		{"1.2.9", "1.3.0", true},
		{"1.9.0", "2.0.0", true},
		{"0.1.17", "0.1.18", false},
		{"0.1.17", "0.2.0", true},
		{"junk", "1.0.0", false},
	}
	for _, c := range cases {
		if got := MinorBump(c.cur, c.next); got != c.want {
			t.Errorf("MinorBump(%s, %s) = %v", c.cur, c.next, got)
		}
	}
}

func TestRenderNotes(t *testing.T) {
	rel := &Release{
		Version:     "1.3.0",
		PublishedAt: time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC),
		Body:        "## What's Changed\n* Big **thing** by [me](https://x)\n  that wraps\n\n- second\n\nA closing `note`\ncontinued.",
	}
	lines := RenderNotes(rel.Notes())
	var texts []string
	for _, l := range lines {
		texts = append(texts, l.Text)
	}
	joined := strings.Join(texts, "\n")
	for _, want := range []string{"v1.3.0  (2026-09-06)", "What's Changed", "Big thing by me that wraps", "second", "A closing note continued."} {
		found := false
		for _, s := range texts {
			if s == want {
				found = true
			}
		}
		if !found {
			t.Errorf("missing line %q in:\n%s", want, joined)
		}
	}
	if !lines[0].Heading {
		t.Error("first line should be the version heading")
	}
	bullets := 0
	for _, l := range lines {
		if l.Bullet {
			bullets++
		}
	}
	if bullets != 2 {
		t.Errorf("want 2 bullets, got %d in:\n%s", bullets, joined)
	}
	var nilRel *Release
	if nilRel.Notes() != nil {
		t.Error("nil release should have no notes")
	}
}
