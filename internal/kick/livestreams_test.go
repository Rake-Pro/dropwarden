package kick

import (
	"strings"
	"testing"
)

// Shape taken from a live kick.com/stream/livestreams response (2026-11-04).
const livestreamsBody = `{"current_page":1,"data":[
 {"id":1,"slug":"stream-a","is_live":true,"viewer_count":11892,"channel":{"id":10,"slug":"AllanStag","user":{"username":"allanstag"}}},
 {"id":2,"slug":"stream-b","is_live":true,"viewer_count":1324,"channel":{"id":11,"slug":"vector","user":{"username":"vector"}}},
 {"id":3,"slug":"stream-c","is_live":false,"viewer_count":0,"channel":{"id":12,"slug":"offline","user":{"username":"offline"}}},
 {"id":4,"slug":"stream-d","is_live":true,"viewer_count":903,"channel":{"id":11,"slug":"vector","user":{"username":"vector"}}}
],"per_page":24}`

func TestParseLivestreams(t *testing.T) {
	got, err := ParseLivestreams([]byte(livestreamsBody))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"allanstag", "vector"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestParseLivestreamsResponseStatus(t *testing.T) {
	if _, err := ParseLivestreamsResponse(`{"status":403,"body":"blocked"}`); err == nil {
		t.Fatal("non-200 must be an error so the scrape fallback runs")
	}
	got, err := ParseLivestreamsResponse(`{"status":200,"body":"{\"data\":[]}"}`)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty category must be an empty list, not an error: %v %v", got, err)
	}
	if !strings.Contains(jsFetchLivestreams("call-of-duty-modern-warfare-4"), "subcategory=call-of-duty-modern-warfare-4&sort=desc") {
		t.Fatal("fetch JS must carry the category slug")
	}
}
