package power

import (
	"errors"
	"runtime"
	"sync"

	"golang.org/x/sys/windows"
)

// ES_* flags for SetThreadExecutionState. ES_DISPLAY_REQUIRED is deliberately
// absent so the display still sleeps.
const (
	esSystemRequired = 0x00000001
	esContinuous     = 0x80000000
)

var (
	kernel32                = windows.NewLazySystemDLL("kernel32.dll")
	procSetThreadExecState  = kernel32.NewProc("SetThreadExecutionState")
	errSetThreadExecStateNo = errors.New("SetThreadExecutionState returned 0")
)

// winHolder keeps ES_SYSTEM_REQUIRED|ES_CONTINUOUS set. The flag is
// per-thread, so a dedicated goroutine locks an OS thread, sets it, and
// waits; Release makes that same thread clear it and exit.
type winHolder struct {
	mu   sync.Mutex
	stop chan struct{}
}

func newHolder() Holder { return &winHolder{} }

func (h *winHolder) Hold() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stop != nil {
		return nil
	}
	stop := make(chan struct{})
	ready := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		r, _, _ := procSetThreadExecState.Call(uintptr(esContinuous | esSystemRequired))
		if r == 0 {
			ready <- errSetThreadExecStateNo
			return
		}
		ready <- nil
		<-stop
		procSetThreadExecState.Call(uintptr(esContinuous))
	}()
	if err := <-ready; err != nil {
		return err
	}
	h.stop = stop
	return nil
}

func (h *winHolder) Release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stop == nil {
		return
	}
	close(h.stop)
	h.stop = nil
}
