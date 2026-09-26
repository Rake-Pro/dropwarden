package twitch

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rake-pro/dropwarden/internal/twitch/gql"
)

// loginPollWindow and loginPollEvery bound CheckLogin's DOM poll: the avatar
// renders after hydration, well after onload, so one read at onload can say
// "logged out" for a session that is logged in. Vars so tests can shrink them.
var (
	loginPollWindow = 10 * time.Second
	loginPollEvery  = time.Second
)

// LoginState describes whether the persistent profile is authenticated.
type LoginState struct {
	LoggedIn       bool `json:"loggedIn"`
	HasLoginButton bool `json:"hasLoginButton"`
}

// CheckLogin navigates to the Twitch home page and reports whether the session
// is authenticated. The profile persists the login across runs, so this is how
// the app decides between "ready" and "prompt the user to log in".
func CheckLogin(ctx context.Context, sess interface {
	Navigate(context.Context, string) error
	Eval(context.Context, string, any) error
}) (LoginState, error) {
	if err := sess.Navigate(ctx, URLBase); err != nil {
		return LoginState{}, fmt.Errorf("twitch: navigate home: %w", err)
	}
	// Poll the DOM until it shows the avatar or a login button (or the window
	// runs out): a single read at onload races hydration.
	var st LoginState
	deadline := time.Now().Add(loginPollWindow)
	for {
		if err := sess.Eval(ctx, jsLoggedIn, &st); err != nil {
			return LoginState{}, fmt.Errorf("twitch: probe login: %w", err)
		}
		if st.LoggedIn || st.HasLoginButton || !time.Now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return LoginState{}, ctx.Err()
		case <-time.After(loginPollEvery):
		}
	}
	if st.LoggedIn {
		return st, nil
	}
	// The DOM says logged out; confirm with a DOM-independent currentUser
	// query so a selector drift cannot report a logged-in session as logged
	// out. An inconclusive answer keeps the DOM verdict.
	if user, known := gqlCurrentUser(ctx, sess); known {
		st.LoggedIn = user
	}
	return st, nil
}

// gqlCurrentUser asks Twitch GraphQL, from page context, who the session is:
// (true, true) when currentUser has a login, (false, true) when currentUser is
// null (anonymous), and known=false when the request failed or was rejected.
func gqlCurrentUser(ctx context.Context, sess interface {
	Eval(context.Context, string, any) error
}) (loggedIn, known bool) {
	body, _ := json.Marshal(map[string]any{"query": "{currentUser{login}}"})
	var raw string
	if err := sess.Eval(ctx, gqlFetchIIFE(body), &raw); err != nil {
		return false, false
	}
	if gql.CurrentUserNull([]byte(raw)) {
		return false, true
	}
	var resp struct {
		Data struct {
			CurrentUser *struct {
				Login string `json:"login"`
			} `json:"currentUser"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil || resp.Data.CurrentUser == nil || resp.Data.CurrentUser.Login == "" {
		return false, false
	}
	return true, true
}

// WaitForLogin polls until the session becomes authenticated or ctx is done.
// It is used by the manual-login flow: the app opens Twitch, the user logs in
// by hand, and this returns once the avatar appears.
func WaitForLogin(ctx context.Context, sess interface {
	Navigate(context.Context, string) error
	Eval(context.Context, string, any) error
}, poll time.Duration) error {
	if poll <= 0 {
		poll = 3 * time.Second
	}
	if _, err := CheckLogin(ctx, sess); err != nil {
		return err
	}
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			var st LoginState
			if err := sess.Eval(ctx, jsLoggedIn, &st); err == nil && st.LoggedIn {
				return nil
			}
		}
	}
}
