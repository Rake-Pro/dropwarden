package twitch

import (
	"context"
	"fmt"
	"time"
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
	var st LoginState
	if err := sess.Eval(ctx, jsLoggedIn, &st); err != nil {
		return LoginState{}, fmt.Errorf("twitch: probe login: %w", err)
	}
	return st, nil
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
