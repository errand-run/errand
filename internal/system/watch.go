package system

import (
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Watch: `on` is called when something is added to a directory or taken from it, and when its file `name` ( there already,
// or to come ) is written -- told by the system, where it tells; looked at twice a second where it does not. What is
// written by another machine of a shared disk is told to nobody. -> what stops it; `on` is not called after.
func Watch(dir, name string, on func()) (stop func()) {
	var mu sync.Mutex
	over := false
	said := func() {
		mu.Lock()
		defer mu.Unlock()
		if !over {
			on()
		}
	}
	end, err := watchDir(dir, name, said)
	if err != nil {
		end = lookAt(dir, name, said)
	}
	return func() {
		mu.Lock()
		was := over
		over = true
		mu.Unlock()
		if !was {
			end()
		}
	}
}

func lookAt(dir, name string, on func()) func() {
	quit := make(chan struct{})
	state := func() (out [2]int64) {
		if st, err := os.Stat(dir); err == nil {
			out[0] = st.ModTime().UnixNano()
		}
		if st, err := os.Stat(filepath.Join(dir, name)); err == nil {
			out[1] = st.Size()
		}
		return out
	}
	go func() {
		last := state()
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-quit:
				return
			case <-tick.C:
			}
			if now := state(); now != last {
				last = now
				on()
			}
		}
	}()
	return func() { close(quit) }
}
