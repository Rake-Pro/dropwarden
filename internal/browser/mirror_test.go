package browser

import (
	"reflect"
	"testing"

	"github.com/go-rod/rod/lib/proto"
)

func intp(v int) *int { return &v }

func TestCDPInputMapping(t *testing.T) {
	const vw, vh = 1280, 720
	cases := []struct {
		name string
		ev   InputEvent
		want cdpRequest
	}{
		{"move scales fractions to the viewport", InputEvent{Type: "mousemove", X: 0.5, Y: 0.25, Buttons: 1},
			proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMouseMoved, X: 640, Y: 180, Button: proto.InputMouseButtonNone, Buttons: intp(1)}},
		{"coordinates clamp to the frame", InputEvent{Type: "mousemove", X: -0.2, Y: 1.7},
			proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMouseMoved, X: 0, Y: 720, Button: proto.InputMouseButtonNone, Buttons: intp(0)}},
		{"left down", InputEvent{Type: "mousedown", X: 1, Y: 1, Button: 0, Buttons: 1},
			proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMousePressed, X: 1280, Y: 720, Button: proto.InputMouseButtonLeft, Buttons: intp(1), ClickCount: 1}},
		{"middle down", InputEvent{Type: "mousedown", Button: 1, Buttons: 4},
			proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMousePressed, Button: proto.InputMouseButtonMiddle, Buttons: intp(4), ClickCount: 1}},
		{"right up", InputEvent{Type: "mouseup", Button: 2},
			proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMouseReleased, Button: proto.InputMouseButtonRight, Buttons: intp(0), ClickCount: 1}},
		{"back and forward", InputEvent{Type: "mouseup", Button: 3},
			proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMouseReleased, Button: proto.InputMouseButtonBack, Buttons: intp(0), ClickCount: 1}},
		{"unknown button is none", InputEvent{Type: "mousedown", Button: 9},
			proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMousePressed, Button: proto.InputMouseButtonNone, Buttons: intp(0), ClickCount: 1}},
		{"shift+ctrl click keeps CDP modifier bits, drops the rest", InputEvent{Type: "mousedown", Button: 0, Modifiers: 8 | 2 | 0x30},
			proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMousePressed, Button: proto.InputMouseButtonLeft, Buttons: intp(0), ClickCount: 1, Modifiers: 10}},
		{"wheel carries deltas", InputEvent{Type: "wheel", X: 0.1, Y: 0.1, DeltaX: 3, DeltaY: -120},
			proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMouseWheel, X: 128, Y: 72, Button: proto.InputMouseButtonNone, Buttons: intp(0), DeltaX: 3, DeltaY: -120}},
		{"keydown is a raw key down", InputEvent{Type: "keydown", Key: "Backspace", Code: "Backspace", KeyCode: 8},
			proto.InputDispatchKeyEvent{Type: proto.InputDispatchKeyEventTypeRawKeyDown, Key: "Backspace", Code: "Backspace", WindowsVirtualKeyCode: 8, NativeVirtualKeyCode: 8}},
		{"enter keydown carries its text", InputEvent{Type: "keydown", Key: "Enter", Code: "Enter", KeyCode: 13},
			proto.InputDispatchKeyEvent{Type: proto.InputDispatchKeyEventTypeKeyDown, Key: "Enter", Code: "Enter", WindowsVirtualKeyCode: 13, NativeVirtualKeyCode: 13, Text: "\r", UnmodifiedText: "\r"}},
		{"ctrl+a keydown keeps modifiers", InputEvent{Type: "keydown", Key: "a", Code: "KeyA", KeyCode: 65, Modifiers: 2},
			proto.InputDispatchKeyEvent{Type: proto.InputDispatchKeyEventTypeRawKeyDown, Key: "a", Code: "KeyA", WindowsVirtualKeyCode: 65, NativeVirtualKeyCode: 65, Modifiers: 2}},
		{"keyup", InputEvent{Type: "keyup", Key: "Tab", Code: "Tab", KeyCode: 9, Modifiers: 8},
			proto.InputDispatchKeyEvent{Type: proto.InputDispatchKeyEventTypeKeyUp, Key: "Tab", Code: "Tab", WindowsVirtualKeyCode: 9, NativeVirtualKeyCode: 9, Modifiers: 8}},
		{"char inserts text", InputEvent{Type: "char", Text: "p@ss"}, proto.InputInsertText{Text: "p@ss"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cdpInput(tc.ev, vw, vh)
			if err != nil {
				t.Fatalf("cdpInput: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v\nwant %#v", got, tc.want)
			}
		})
	}
}

func TestCDPInputRejects(t *testing.T) {
	cases := []struct {
		name   string
		ev     InputEvent
		vw, vh float64
	}{
		{"pointer before any frame", InputEvent{Type: "mousedown"}, 0, 0},
		{"empty char", InputEvent{Type: "char"}, 100, 100},
		{"unknown type", InputEvent{Type: "touch"}, 100, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := cdpInput(tc.ev, tc.vw, tc.vh); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
