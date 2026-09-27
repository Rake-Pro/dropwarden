package browser

import (
	"fmt"

	"github.com/rake-pro/dropwarden/internal/config"
)

// chromeFlags builds the command line for the dedicated watcher browser.
//
// Two properties of this set are load-bearing and must not be "tidied up":
//
//   - The backgrounding flags plus CalculateNativeWinOcclusion keep the video
//     element decoding while the window is covered or off to the side. Without
//     them Chrome throttles the tab and drop progress stops accruing.
//   - The omissions matter as much as the inclusions. --enable-automation and
//     --headless are the two loudest bot tells on the platform, and --no-sandbox
//     is a gratuitous security downgrade for a browser that loads untrusted
//     pages.
//   - --disable-blink-features=AutomationControlled is REQUIRED. A Chrome
//     launched with --remote-debugging-port alone still reports
//     navigator.webdriver === true (live 2026-09-26: v0.2.2 dropped the flag
//     and within a day Twitch answered every integrity-gated query, the page's
//     own included, with IntegrityCheckFailed; the campaigns page went empty
//     and no drop could be claimed). Chrome shows an "unsupported
//     command-line flag" bar for it, which --test-type suppresses (the same
//     pair ChromeDriver ships); --test-type is not visible to page scripts.
//
// --mute-audio is OPT-IN (browser.mute_audio): it silences the dedicated
// instance at the audio sink while the player element itself stays unmuted at
// 1%, so the page cannot tell. The design-phase concern that Chrome pauses
// inaudible background media has not been observed live; if it does happen, the
// frozen-video and stall watchdogs catch and recover it, and the toggle turns
// it back off.
//
// The trailing about:blank gives Chrome a real startup page, so the CDP target
// list has exactly one page for the session to adopt.
func chromeFlags(cfg config.BrowserConfig, profileDir string, w config.WindowConfig) []string {
	flags := []string{
		"--user-data-dir=" + profileDir,
		"--remote-debugging-port=0",
		"--disable-blink-features=AutomationControlled",
		"--test-type",
		fmt.Sprintf("--window-size=%d,%d", w.Width, w.Height),
		fmt.Sprintf("--window-position=%d,%d", w.X, w.Y),
		"--disable-background-timer-throttling",
		"--disable-backgrounding-occluded-windows",
		"--disable-renderer-backgrounding",
		"--disable-features=CalculateNativeWinOcclusion",
		"--autoplay-policy=no-user-gesture-required",
		"--no-first-run",
		"--no-default-browser-check",
		"--no-service-autorun",
	}
	if cfg.MuteAudio {
		flags = append(flags, "--mute-audio")
	}
	flags = append(flags, cfg.ExtraFlags...)
	return append(flags, "about:blank")
}
