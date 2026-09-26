package twitch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rake-pro/dropwarden/internal/browser"
	"github.com/rake-pro/dropwarden/internal/domain"
)

// loginSess scripts CheckLogin: the DOM probe answers from dom (one entry per
// call, the last repeats) and the currentUser GQL from gqlRaw.
type loginSess struct {
	dom      []LoginState
	domCalls int
	gqlRaw   string
	gqlCalls int
}

func (s *loginSess) Navigate(context.Context, string) error { return nil }
func (s *loginSess) Eval(_ context.Context, js string, out any) error {
	if js == jsLoggedIn {
		st := s.dom[min(s.domCalls, len(s.dom)-1)]
		s.domCalls++
		*(out.(*LoginState)) = st
		return nil
	}
	s.gqlCalls++
	*(out.(*string)) = s.gqlRaw
	return nil
}

func shortLoginPoll(t *testing.T) {
	t.Helper()
	w, e := loginPollWindow, loginPollEvery
	loginPollWindow, loginPollEvery = 200*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { loginPollWindow, loginPollEvery = w, e })
}

func TestCheckLoginPollsUntilAvatarRenders(t *testing.T) {
	shortLoginPoll(t)
	s := &loginSess{dom: []LoginState{{}, {}, {LoggedIn: true}}}
	st, err := CheckLogin(context.Background(), s)
	if err != nil || !st.LoggedIn || s.domCalls != 3 || s.gqlCalls != 0 {
		t.Fatalf("late avatar must read logged in without GQL: %+v err=%v dom=%d gql=%d", st, err, s.domCalls, s.gqlCalls)
	}
}

func TestCheckLoginConfirmsDOMVerdictWithGQL(t *testing.T) {
	shortLoginPoll(t)
	// DOM shows a login button (stale selector), but currentUser is present.
	s := &loginSess{dom: []LoginState{{HasLoginButton: true}}, gqlRaw: `{"data":{"currentUser":{"login":"me"}}}`}
	if st, err := CheckLogin(context.Background(), s); err != nil || !st.LoggedIn {
		t.Fatalf("currentUser present must override the DOM: %+v %v", st, err)
	}
	s = &loginSess{dom: []LoginState{{HasLoginButton: true}}, gqlRaw: `{"data":{"currentUser":null}}`}
	if st, err := CheckLogin(context.Background(), s); err != nil || st.LoggedIn || s.gqlCalls != 1 {
		t.Fatalf("currentUser null confirms logged out: %+v %v gql=%d", st, err, s.gqlCalls)
	}
}

func TestLiveChannelsChunksAndSkipsFailedBatch(t *testing.T) {
	logins := make([]string, 250)
	for i := range logins {
		logins[i] = fmt.Sprintf("u%03d", i)
	}
	var sizes []int
	got, err := liveChannelsChunked(logins, func(chunk []string) ([]domain.Channel, error) {
		sizes = append(sizes, len(chunk))
		if strings.HasPrefix(chunk[0], "u1") { // the second batch (u100..u199) fails
			return nil, errors.New("boom")
		}
		return []domain.Channel{{Login: chunk[0], Live: true}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 3 || sizes[0] != 100 || sizes[1] != 100 || sizes[2] != 50 {
		t.Fatalf("batches of 100 expected, got %v", sizes)
	}
	if len(got) != 2 || got[0].Login != "u000" || got[1].Login != "u200" {
		t.Fatalf("results of the good batches must merge, failed batch skipped: %+v", got)
	}
	if _, err := liveChannelsChunked(logins[:5], func([]string) ([]domain.Channel, error) { return nil, errors.New("down") }); err == nil {
		t.Fatal("when every batch fails the error must surface")
	}
}

// TestLiveChannelsPageBatches drives the real page call over a fake session
// that answers each batch with its own users.
func TestLiveChannelsPageBatches(t *testing.T) {
	logins := make([]string, 150)
	for i := range logins {
		logins[i] = fmt.Sprintf("c%03d", i)
	}
	calls := 0
	sess := &batchSess{answer: func(js string) string {
		calls++
		// Mark the first login of each batch live.
		first := "c000"
		if strings.Contains(js, `c100`) {
			first = "c100"
		}
		b, _ := json.Marshal(map[string]any{"data": map[string]any{"users": []any{
			map[string]any{"id": "1", "login": first, "stream": map[string]any{"id": "s", "viewersCount": 5}},
		}}})
		return string(b)
	}}
	p := NewPages(&controller{sess: sess})
	got, err := p.LiveChannels(context.Background(), logins)
	if err != nil || calls != 2 || len(got) != 2 {
		t.Fatalf("150 logins = 2 calls, 2 live: calls=%d got=%+v err=%v", calls, got, err)
	}
}

// batchSess is a browser.Session answering every Eval from answer.
type batchSess struct{ answer func(js string) string }

func (s *batchSess) Navigate(context.Context, string) error          { return nil }
func (s *batchSess) Reload(context.Context, bool) error              { return nil }
func (s *batchSess) CurrentURL(context.Context) (string, error)      { return "", nil }
func (s *batchSess) Click(context.Context, string) (bool, error)     { return false, nil }
func (s *batchSess) SeedOnNewDocument(context.Context, string) error { return nil }
func (s *batchSess) Events() <-chan browser.Event                    { return nil }
func (s *batchSess) ShiftWindow(int, int) error                      { return nil }
func (s *batchSess) SetMinimized(context.Context, bool) error        { return nil }
func (s *batchSess) Alive() bool                                     { return true }
func (s *batchSess) Close() error                                    { return nil }
func (s *batchSess) Eval(_ context.Context, js string, out any) error {
	*(out.(*string)) = s.answer(js)
	return nil
}
