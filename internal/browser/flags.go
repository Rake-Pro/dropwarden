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
//     --headless are the two loudest bot tells on the platform, --mute-audio
//     makes Chrome treat the video as inaudible background media and pause it,
//     and --no-sandbox is a gratuitous security downgrade for a browser that
//     loads untrusted pages.
//
// The trailing about:blank gives Chrome a real startup page, so the CDP target
// list has exactly one page for the session to adopt.
func chromeFlags(cfg config.BrowserConfig, profileDir string, w config.WindowConfig) []string {
	flags := []string{
		"--user-data-dir=" + profileDir,
		"--remote-debugging-port=0",
		fmt.Sprintf("--window-size=%d,%d", w.Width, w.Height),
		fmt.Sprintf("--window-position=%d,%d", w.X, w.Y),
		"--disable-background-timer-throttling",
		"--disable-backgrounding-occluded-windows",
		"--disable-renderer-backgrounding",
		"--disable-features=CalculateNativeWinOcclusion",
		"--autoplay-policy=no-user-gesture-required",
		"--disable-blink-features=AutomationControlled",
		"--no-first-run",
		"--no-default-browser-check",
		"--no-service-autorun",
	}
	flags = append(flags, cfg.ExtraFlags...)
	return append(flags, "about:blank")
}
