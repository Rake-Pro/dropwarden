package queue

import (
	"strings"
	"testing"

	"github.com/rake-pro/dropwarden/internal/domain"
)

func drop(id string, preconditions ...string) domain.Drop {
	return domain.Drop{ID: id, CampaignID: "camp1", PreconditionIDs: preconditions}
}

func idsOf(drops []domain.Drop) []string {
	ids := make([]string, len(drops))
	for i, d := range drops {
		ids[i] = d.ID
	}
	return ids
}

func TestSortByPrecondition_LinearChain(t *testing.T) {
	in := []domain.Drop{
		drop("c", "b"),
		drop("a"),
		drop("b", "a"),
	}
	got, err := SortByPrecondition(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"a", "b", "c"}
	if got := idsOf(got); !equalSlices(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSortByPrecondition_Diamond(t *testing.T) {
	// a -> b, a -> c, b+c -> d
	in := []domain.Drop{
		drop("d", "b", "c"),
		drop("a"),
		drop("b", "a"),
		drop("c", "a"),
	}
	got, err := SortByPrecondition(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	gotIDs := idsOf(got)
	pos := map[string]int{}
	for i, id := range gotIDs {
		pos[id] = i
	}
	if pos["a"] > pos["b"] || pos["a"] > pos["c"] || pos["b"] > pos["d"] || pos["c"] > pos["d"] {
		t.Fatalf("ordering violates preconditions: %v", gotIDs)
	}
	// deterministic: b before c since b appears before c in input among ties
	if pos["b"] > pos["c"] {
		t.Fatalf("expected deterministic tie-break b before c, got %v", gotIDs)
	}
}

func TestSortByPrecondition_Cycle(t *testing.T) {
	in := []domain.Drop{
		drop("a", "c"),
		drop("b", "a"),
		drop("c", "b"),
	}
	_, err := SortByPrecondition(in)
	if err == nil {
		t.Fatal("expected error for cycle, got nil")
	}
	for _, id := range []string{"a", "b", "c"} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("expected error to name %q, got %v", id, err)
		}
	}
	if !strings.HasPrefix(err.Error(), "queue: ") {
		t.Errorf("expected error prefix 'queue: ', got %v", err)
	}
}

func TestSortByPrecondition_ExternalOrMissingPreconditionIgnored(t *testing.T) {
	in := []domain.Drop{
		drop("a", "does-not-exist"),
		drop("b", "a", "also-missing"),
	}
	got, err := SortByPrecondition(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"a", "b"}
	if gotIDs := idsOf(got); !equalSlices(gotIDs, want) {
		t.Fatalf("got %v, want %v", gotIDs, want)
	}
}

func TestSortByPrecondition_DeterministicNoPreconditions(t *testing.T) {
	in := []domain.Drop{drop("z"), drop("y"), drop("x")}
	got, err := SortByPrecondition(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"z", "y", "x"}
	if gotIDs := idsOf(got); !equalSlices(gotIDs, want) {
		t.Fatalf("got %v, want %v (original order preserved)", gotIDs, want)
	}
}

func TestSortByPrecondition_Empty(t *testing.T) {
	got, err := SortByPrecondition(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty result, got %v", got)
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
