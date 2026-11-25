package page

import (
	"testing"
	"time"
)

// A request that lasts ( a service that starts slowly, a WebSocket ) is a use until it is answered.
func TestARequestBeingAnsweredIsAUse(t *testing.T) {
	long := time.Now().Add(-time.Hour)
	w := &webServer{last: long}
	if w.idle() < time.Hour {
		t.Errorf("nobody asked: idle for %v", w.idle())
	}
	a, b := w.inUse(), w.inUse()
	w.last = long
	if a(); w.idle() != 0 {
		t.Errorf("one of two answered: idle for %v", w.idle())
	}
	w.last = long
	if b(); w.idle() > time.Minute {
		t.Errorf("just answered: idle for %v", w.idle())
	}
	if w.last = long; w.idle() < time.Hour {
		t.Errorf("answered long ago: idle for %v", w.idle())
	}
}
