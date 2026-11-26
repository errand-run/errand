// `errand there --only key:NAME` ( ADR 0040 ): the far side of a link whose key may reach only the servers authorized to
// it. What comes is read here, in the process the key's forced command starts, and only that is passed on to the agent:
// `hello`, and a connection to a server or a look at it, by the hash of its authorization -- to which nothing is added
// but the key's name. Everything else is refused, an errand sent to replace this one included.

package reach

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/errand-run/errand/internal/kit"
)

var (
	keyName  = regexp.MustCompile(`^key:[A-Za-z0-9_.@-]+$`)
	hashWord = regexp.MustCompile(`^[0-9a-f]{8}$`)
)

func thereOnly(only string) int {
	l, err := linkHere() // ( the agent is started when it is not there )
	if err != nil {
		fmt.Fprintln(os.Stderr, "errand:", err)
		return 1
	}
	l.Close()
	ServeLink(os.Stdin, os.Stdout, func(req map[string]any, f *FarChan) {
		switch op := kit.Text(req["op"]); op {
		case "hello": // ( not what it holds, nor where )
			l, err := linkHere()
			if err != nil {
				f.Fail("%v", err)
				return
			}
			defer l.Close()
			got, err := l.Ask(map[string]any{"op": "hello"})
			if err != nil {
				f.Fail("%v", err)
				return
			}
			f.Reply(map[string]any{"ok": true, "version": got["version"], "release": got["release"], "host": got["host"], "only": only})
		case "service_connect", "service_look":
			hash := kit.Text(req["hash"])
			if !hashWord.MatchString(hash) {
				f.Fail("this link is for %s: a server is asked for by the hash of its authorization ( errand serve authorized, there )", only)
				return
			}
			clean := map[string]any{"op": op, "hash": hash, "only": only}
			if port := kit.Text(req["port"]); port != "" {
				clean["port"] = port
			}
			Pass(clean, f, op == "service_connect")
		default:
			f.Fail("this link is for %s: it reaches the servers authorized to it, nothing else ( %q is refused )", only, op)
		}
	})
	return 0
}

// Pass: a request passed on to this machine's agent, and what it answers passed back -- both ways, until it is through
func Pass(req any, f *FarChan, stream bool) {
	l, err := linkHere()
	if err != nil {
		f.Fail("%v", err)
		return
	}
	defer l.Close()
	c := l.Open(req)
	defer c.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case m := <-f.Inbox:
				_ = c.Send(m.Kind, m.Payload)
			case <-f.Gone: // ( the agent sees its channel go with its link )
				l.Close()
				return
			case <-done:
				return
			}
		}
	}()
	for {
		kind, payload, err := c.Recv()
		if err != nil {
			if stream {
				f.Out(kit.EXIT, []byte(`{"code":1}`))
			} else {
				f.Fail("%s: %v", l.Where, err)
			}
			return
		}
		f.Out(kind, payload)
		if kind == kit.EXIT || kind == kit.REPLY && (!stream || strings.Contains(string(payload), `"error"`)) {
			return
		}
	}
}
