package reach

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/errand-run/errand/internal/kit"
)

var ErrStopped = errors.New("stopped")

// AskHeld: the same; `hold`, when it is not nil, keeps the channel open once it is answered
func AskHeld(t *Term, l *Link, req map[string]any, quiet bool, hold *[]*Call) (got_ map[string]any, err_ error) {
	c := l.Open(req)
	told := true
	defer func() {
		if err_ == nil && hold != nil {
			*hold = append(*hold, c)
		} else if told {
			c.Close()
		} else {
			c.Abandon()
		}
	}()
	got := make(chan LinkMsg, 16)
	go func() {
		defer close(got)
		for {
			kind, payload, err := c.Recv()
			if err != nil {
				return
			}
			got <- LinkMsg{kind, payload}
			if kind == kit.REPLY {
				return
			}
		}
	}()
	for {
		select {
		case <-t.Sigs:
			return nil, ErrStopped
		case <-t.Gone:
			told = false
			return nil, errors.New("whoever asked went")
		case m, ok := <-got:
			if !ok {
				return nil, fmt.Errorf("%s: %w", l.Where, errLinkLost)
			}
			switch m.Kind {
			case kit.NOTE:
				var said map[string]any
				_ = json.Unmarshal(m.Payload, &said)
				if text, _ := said["say"].(string); text != "" && !quiet {
					t.Say(text)
				}
			case kit.REPLY:
				reply := map[string]any{}
				dec := json.NewDecoder(bytes.NewReader(m.Payload))
				dec.UseNumber()
				if err := dec.Decode(&reply); err != nil {
					return nil, fmt.Errorf("%s: an answer that cannot be read: %v", l.Where, err)
				}
				if said, ok := reply["error"]; ok {
					return nil, fmt.Errorf("%s: %v", l.Where, said)
				}
				return reply, nil
			}
		}
	}
}
