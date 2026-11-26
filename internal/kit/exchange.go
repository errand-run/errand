package kit

import (
	"sync"
	"time"
)

// Exchange is what was said last on a connection: whether the server owes an answer, and since when it is silent. It holds
// back the end of a client's input ( `hold_end` ), for servers that take that end for a leave.
type Exchange struct {
	mu   sync.Mutex
	owed bool
	last time.Time
}

func (e *Exchange) Asked() {
	e.mu.Lock()
	e.owed = true
	e.mu.Unlock()
}

func (e *Exchange) Answered() {
	e.mu.Lock()
	e.owed, e.last = false, time.Now()
	e.mu.Unlock()
}

// Over: waits until the server, which may think as long as it wants, has answered and been silent for `quiet`; false when
// the connection ended before
func (e *Exchange) Over(quiet time.Duration, ended <-chan struct{}) bool {
	for {
		e.mu.Lock()
		owed, left := e.owed, quiet-time.Since(e.last)
		e.mu.Unlock()
		switch {
		case owed:
			left = 100 * time.Millisecond
		case left <= 0:
			return true
		}
		select {
		case <-time.After(min(left, 100*time.Millisecond)):
		case <-ended:
			return false
		}
	}
}
