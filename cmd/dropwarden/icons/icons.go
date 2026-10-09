// Package icons embeds the tray and Dock icons that scripts/gen-icons.py
// writes next to this file: one chest per status level, the non-idle ones
// with a coloured dot in the corner.
package icons

import "embed"

//go:embed *.png
var files embed.FS

// byName holds every PNG, read once at init: Wails keeps a pointer to the
// bytes it is handed (macOS reads them later on the main queue), so the
// slices must live for the whole process rather than be re-read per call.
var byName = map[string][]byte{}

func init() {
	entries, err := files.ReadDir(".")
	if err != nil {
		panic("icons: " + err.Error())
	}
	for _, e := range entries {
		b, err := files.ReadFile(e.Name())
		if err != nil {
			panic("icons: " + err.Error())
		}
		byName[e.Name()] = b
	}
}

// Level is the one-word summary of every engine's state that the tray and
// Dock icons show.
type Level string

const (
	Idle      Level = "idle"      // running, nothing to report (plain chest)
	Watching  Level = "watching"  // at least one engine is mid-watch (green)
	Paused    Level = "paused"    // nothing watching: paused, or backing off (amber)
	Attention Level = "attention" // an engine needs the user (red)
)

// Tray returns the 44x44 menu-bar / system-tray icon for a level.
func Tray(l Level) []byte { return read("tray_" + string(l) + ".png") }

// Dock returns the 512x512 application icon for a level (macOS Dock, About
// dialog).
func Dock(l Level) []byte { return read("dock_" + string(l) + ".png") }

func read(name string) []byte {
	b, ok := byName[name]
	if !ok {
		// Every level's file is generated and committed; a missing one is a
		// build error, not a runtime condition.
		panic("icons: missing " + name)
	}
	return b
}
