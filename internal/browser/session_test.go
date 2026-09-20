package browser

import (
	"errors"
	"fmt"
	"testing"

	"github.com/go-rod/rod/lib/cdp"
)

func TestIsNavigatedAway(t *testing.T) {
	nav := &cdp.Error{Code: -32000, Message: "Inspected target navigated or closed"}
	if !isNavigatedAway(nav) {
		t.Fatal("bare navigated error not recognized")
	}
	if !isNavigatedAway(fmt.Errorf("wrap: %w", nav)) {
		t.Fatal("wrapped navigated error not recognized")
	}
	if isNavigatedAway(&cdp.Error{Code: -32000, Message: "Target closed"}) {
		t.Fatal("target closed must not be retried")
	}
	if isNavigatedAway(&cdp.Error{Code: -32602, Message: "Inspected target navigated or closed"}) {
		t.Fatal("other codes must not be retried")
	}
	if isNavigatedAway(errors.New("context deadline exceeded")) {
		t.Fatal("non-CDP errors must not be retried")
	}
}
