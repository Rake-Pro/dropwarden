package browser

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-rod/rod/lib/proto"
)

// Mirror is the remote-view capability of a real Chrome tab: a JPEG screencast
// of the tab plus input replay, both over CDP, so a dashboard in another
// browser can drive a login on the dedicated window. It is optional: the rod
// tab (and Managed around it) implements it, test fakes do not. Neither method
// activates the tab or focuses the OS window.
type Mirror interface {
	// Screencast streams the tab's frames to onFrame until ctx ends, then
	// stops the screencast. It blocks; onFrame runs on the CDP event loop and
	// must not block.
	Screencast(ctx context.Context, opts MirrorOptions, onFrame func(Frame)) error
	// DispatchInput replays one dashboard input event on the tab. Pointer
	// events need a frame first: their coordinates are fractions of it.
	DispatchInput(ctx context.Context, ev InputEvent) error
}

// MirrorOptions bounds the screencast frame size in device pixels.
type MirrorOptions struct {
	MaxWidth, MaxHeight int
}

// Frame is one screencast frame: a JPEG plus the viewport size (CSS pixels)
// it shows.
type Frame struct {
	JPEG          []byte
	Width, Height int
}

// InputEvent is one input event from the dashboard mirror, in DOM terms.
type InputEvent struct {
	// Type is mousemove, mousedown, mouseup, wheel, keydown, keyup or char.
	Type string `json:"type"`
	// X and Y are fractions (0..1) of the mirrored frame.
	X float64 `json:"x"`
	Y float64 `json:"y"`
	// Button is DOM MouseEvent.button (0 left, 1 middle, 2 right, 3 back,
	// 4 forward); Buttons is DOM MouseEvent.buttons, whose bits CDP shares.
	Button  int     `json:"button"`
	Buttons int     `json:"buttons"`
	DeltaX  float64 `json:"deltaX"`
	DeltaY  float64 `json:"deltaY"`
	Key     string  `json:"key"`
	Code    string  `json:"code"`
	KeyCode int     `json:"keyCode"`
	// Text is the inserted text of a char event.
	Text string `json:"text"`
	// Modifiers uses the CDP bit layout: Alt=1, Ctrl=2, Meta=4, Shift=8.
	Modifiers int `json:"modifiers"`
}

// cdpRequest is a proto request with no result, sendable on a page.
type cdpRequest interface {
	Call(c proto.Client) error
}

var errNoFrame = errors.New("browser: no mirrored frame yet")

// cdpInput maps a dashboard input event to the CDP request that replays it on
// a viewport of vw x vh CSS pixels.
func cdpInput(ev InputEvent, vw, vh float64) (cdpRequest, error) {
	mods := ev.Modifiers & 0xf
	switch ev.Type {
	case "mousemove", "mousedown", "mouseup", "wheel":
		if vw <= 0 || vh <= 0 {
			return nil, errNoFrame
		}
		buttons := ev.Buttons & 0x1f
		m := proto.InputDispatchMouseEvent{
			X:         clamp01(ev.X) * vw,
			Y:         clamp01(ev.Y) * vh,
			Modifiers: mods,
			Button:    proto.InputMouseButtonNone,
			Buttons:   &buttons,
		}
		switch ev.Type {
		case "mousemove":
			m.Type = proto.InputDispatchMouseEventTypeMouseMoved
		case "mousedown":
			m.Type = proto.InputDispatchMouseEventTypeMousePressed
			m.Button = mouseButton(ev.Button)
			m.ClickCount = 1
		case "mouseup":
			m.Type = proto.InputDispatchMouseEventTypeMouseReleased
			m.Button = mouseButton(ev.Button)
			m.ClickCount = 1
		case "wheel":
			m.Type = proto.InputDispatchMouseEventTypeMouseWheel
			m.DeltaX, m.DeltaY = ev.DeltaX, ev.DeltaY
		}
		return m, nil
	case "keydown", "keyup":
		k := proto.InputDispatchKeyEvent{
			Type:                  proto.InputDispatchKeyEventTypeKeyUp,
			Modifiers:             mods,
			Key:                   ev.Key,
			Code:                  ev.Code,
			WindowsVirtualKeyCode: ev.KeyCode,
			NativeVirtualKeyCode:  ev.KeyCode,
		}
		if ev.Type == "keydown" {
			// rawKeyDown carries no text, so it never types; printable keys
			// arrive as char events instead. Enter is the exception: a form's
			// implicit submission needs the keypress its "\r" text produces.
			k.Type = proto.InputDispatchKeyEventTypeRawKeyDown
			if ev.Key == "Enter" {
				k.Type = proto.InputDispatchKeyEventTypeKeyDown
				k.Text, k.UnmodifiedText = "\r", "\r"
			}
		}
		return k, nil
	case "char":
		if ev.Text == "" {
			return nil, fmt.Errorf("browser: empty char event")
		}
		return proto.InputInsertText{Text: ev.Text}, nil
	}
	return nil, fmt.Errorf("browser: unknown input event %q", ev.Type)
}

// mouseButton maps DOM MouseEvent.button to the CDP button name.
func mouseButton(b int) proto.InputMouseButton {
	switch b {
	case 0:
		return proto.InputMouseButtonLeft
	case 1:
		return proto.InputMouseButtonMiddle
	case 2:
		return proto.InputMouseButtonRight
	case 3:
		return proto.InputMouseButtonBack
	case 4:
		return proto.InputMouseButtonForward
	}
	return proto.InputMouseButtonNone
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// Screencast implements Mirror on the rod tab: Page.startScreencast, every
// frame acked so Chrome keeps sending, Page.stopScreencast once ctx ends.
func (t *tab) Screencast(ctx context.Context, opts MirrorOptions, onFrame func(Frame)) error {
	p := t.page.Context(ctx)
	// Subscribe before starting so the first frame is not missed.
	wait := p.EachEvent(func(e *proto.PageScreencastFrame) {
		if err := (proto.PageScreencastFrameAck{SessionID: e.SessionID}).Call(p); err != nil {
			t.logger.Debug().Err(err).Msg("mirror: frame ack failed")
		}
		f := Frame{JPEG: e.Data}
		if md := e.Metadata; md != nil {
			f.Width, f.Height = int(md.DeviceWidth), int(md.DeviceHeight)
			t.mirrorMu.Lock()
			t.viewW, t.viewH = md.DeviceWidth, md.DeviceHeight
			t.mirrorMu.Unlock()
		}
		onFrame(f)
	})
	quality, every := 60, 1
	start := proto.PageStartScreencast{
		Format:        proto.PageStartScreencastFormatJpeg,
		Quality:       &quality,
		EveryNthFrame: &every,
	}
	if opts.MaxWidth > 0 {
		start.MaxWidth = &opts.MaxWidth
	}
	if opts.MaxHeight > 0 {
		start.MaxHeight = &opts.MaxHeight
	}
	if err := start.Call(p); err != nil {
		return fmt.Errorf("browser: start screencast: %w", err)
	}
	t.logger.Info().Msg("mirror: screencast started")
	wait()
	// p's context is done by now; stop on the tab's own lifetime context.
	if err := (proto.PageStopScreencast{}).Call(t.page); err != nil {
		t.logger.Debug().Err(err).Msg("mirror: stop screencast failed")
	}
	t.logger.Info().Msg("mirror: screencast stopped")
	return nil
}

// DispatchInput implements Mirror on the rod tab, scaling pointer coordinates
// to the viewport of the latest screencast frame.
func (t *tab) DispatchInput(ctx context.Context, ev InputEvent) error {
	t.mirrorMu.Lock()
	vw, vh := t.viewW, t.viewH
	t.mirrorMu.Unlock()
	req, err := cdpInput(ev, vw, vh)
	if err != nil {
		return err
	}
	if err := req.Call(t.page.Context(ctx)); err != nil {
		return fmt.Errorf("browser: dispatch %s: %w", ev.Type, err)
	}
	return nil
}

// Screencast implements Mirror on the CURRENT tab; a relaunch ends it.
func (m *Managed) Screencast(ctx context.Context, opts MirrorOptions, onFrame func(Frame)) error {
	s, err := m.guard()
	if err != nil {
		return err
	}
	mi, ok := s.(Mirror)
	if !ok {
		return errors.ErrUnsupported
	}
	return mi.Screencast(ctx, opts, onFrame)
}

// DispatchInput implements Mirror on the CURRENT tab.
func (m *Managed) DispatchInput(ctx context.Context, ev InputEvent) error {
	s, err := m.guard()
	if err != nil {
		return err
	}
	mi, ok := s.(Mirror)
	if !ok {
		return errors.ErrUnsupported
	}
	return mi.DispatchInput(ctx, ev)
}

var (
	_ Mirror = (*tab)(nil)
	_ Mirror = (*Managed)(nil)
)
