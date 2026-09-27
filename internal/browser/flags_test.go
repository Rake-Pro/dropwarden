package browser

import (
	"slices"
	"strings"
	"testing"

	"github.com/rake-pro/dropwarden/internal/config"
)

func testFlags() []string {
	return chromeFlags(
		config.BrowserConfig{ExtraFlags: []string{"--lang=en-US"}},
		"/profiles/dropwarden",
		config.WindowConfig{Width: 640, Height: 400, X: 40, Y: 40},
	)
}

func TestChromeFlagsRequired(t *testing.T) {
	got := testFlags()
	want := []string{
		"--user-data-dir=/profiles/dropwarden",
		"--remote-debugging-port=0",
		"--disable-blink-features=AutomationControlled",
		"--test-type",
		"--window-size=640,400",
		"--window-position=40,40",
		"--disable-background-timer-throttling",
		"--disable-backgrounding-occluded-windows",
		"--disable-renderer-backgrounding",
		"--disable-features=CalculateNativeWinOcclusion",
		"--autoplay-policy=no-user-gesture-required",
		"--no-first-run",
		"--no-default-browser-check",
		"--no-service-autorun",
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("missing required flag %s\ngot: %v", w, got)
		}
	}
}

// The absent flags matter as much as the present ones: --enable-automation and
// --headless are bot tells, --mute-audio is strictly opt-in via
// browser.mute_audio, --no-sandbox is an unnecessary security downgrade, and
// --disable-blink-features triggers Chrome's unsupported-flag bar.
func TestChromeFlagsForbidden(t *testing.T) {
	got := testFlags()
	forbidden := []string{"--enable-automation", "--headless", "--mute-audio", "--no-sandbox"}
	for _, f := range forbidden {
		for _, g := range got {
			if g == f || strings.HasPrefix(g, f+"=") {
				t.Errorf("forbidden flag %s present in %v", f, got)
			}
		}
	}
}

func TestChromeFlagsMuteAudioOptIn(t *testing.T) {
	got := chromeFlags(
		config.BrowserConfig{MuteAudio: true},
		"/profiles/dropwarden",
		config.WindowConfig{Width: 640, Height: 400},
	)
	if !slices.Contains(got, "--mute-audio") {
		t.Errorf("mute_audio: true must add --mute-audio: %v", got)
	}
}

func TestChromeFlagsExtraAndStartupPage(t *testing.T) {
	got := testFlags()
	if !slices.Contains(got, "--lang=en-US") {
		t.Errorf("ExtraFlags not appended: %v", got)
	}
	if got[len(got)-1] != "about:blank" {
		t.Errorf("last argument = %q, want about:blank", got[len(got)-1])
	}
	// The startup URL must stay last so it is not parsed as a flag value.
	if idx := slices.Index(got, "--lang=en-US"); idx == -1 || idx != len(got)-2 {
		t.Errorf("ExtraFlags must sit before the startup URL: %v", got)
	}
}
