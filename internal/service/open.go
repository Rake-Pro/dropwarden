package service

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/rake-pro/dropwarden/internal/browser"
	"github.com/rake-pro/dropwarden/internal/domain"
)

// openTabTimeout bounds creating the background tab in the watcher Chrome.
const openTabTimeout = 10 * time.Second

// Replaceable in tests.
var (
	openInSession = browser.OpenBackgroundTab
	openSystem    = openSystemBrowser
)

// allowedOpenURL reports whether u may be opened from the UI: https on
// twitch.tv, kick.com or one of their subdomains. OpenURL additionally
// accepts a stored campaign's exact account-link URL (campaignLinkURL).
func allowedOpenURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" {
		return nil, fmt.Errorf("only https links on twitch.tv or kick.com can be opened")
	}
	h := strings.ToLower(u.Hostname())
	for _, d := range []string{"twitch.tv", "kick.com"} {
		if h == d || strings.HasSuffix(h, "."+d) {
			return u, nil
		}
	}
	return nil, fmt.Errorf("only https links on twitch.tv or kick.com can be opened")
}

// campaignLinkURL accepts an https URL that is exactly the account-link page
// of one of the profile's stored campaigns (a game publisher's website, which
// the twitch.tv/kick.com rule does not cover).
func (b *Backend) campaignLinkURL(profile, raw string) (*url.URL, bool) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" {
		return nil, false
	}
	pb := b.Profile(profile)
	if pb == nil || pb.Store == nil {
		return nil, false
	}
	campaigns, err := pb.Store.ListCampaigns()
	if err != nil {
		return nil, false
	}
	for _, c := range campaigns {
		if c.AccountLinkURL != "" && strings.TrimSpace(c.AccountLinkURL) == raw {
			return u, true
		}
	}
	return nil, false
}

// OpenURL opens an allowed URL as a new BACKGROUND tab in the profile and
// platform's watcher Chrome when it is up (the watching tab is never touched
// and nothing is brought to the front), else in the system browser. The
// headless service refuses: its Chrome has no screen to show a tab on and
// there is no desktop browser, so the dashboard opens such links itself.
func (b *Backend) OpenURL(ctx context.Context, profile, platform, raw string) error {
	u, err := allowedOpenURL(raw)
	if err != nil {
		link, ok := b.campaignLinkURL(profile, raw)
		if !ok {
			return err
		}
		u = link
	}
	link := u.String()
	if b.Remote() {
		return fmt.Errorf("the headless service has no browser to show %s in: open it in your own browser", link)
	}
	if pb := b.Profile(profile); pb != nil {
		if sess := pb.Session(domain.Platform(platform)); sess != nil && sess.Alive() {
			tctx, cancel := context.WithTimeout(ctx, openTabTimeout)
			err := openInSession(tctx, sess, link)
			cancel()
			if err == nil {
				return nil
			}
		}
	}
	return openSystem(link)
}

// openSystemBrowser hands a URL to the OS default browser.
func openSystemBrowser(link string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", link)
	case "darwin":
		cmd = exec.Command("open", link)
	default:
		cmd = exec.Command("xdg-open", link)
	}
	if err := cmd.Start(); err != nil {
		if runtime.GOOS == "windows" {
			// "start" treats its first quoted argument as a window title.
			if err2 := exec.Command("cmd", "/c", "start", "", link).Start(); err2 == nil {
				return nil
			}
		}
		return fmt.Errorf("open %s in the system browser: %w", link, err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
