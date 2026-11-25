// Sharing a machine, from the command line ( the guide's "Sharing a machine"; the queue itself: package queue ):
//
//	errand queue                 what the machine has, and what holds it
//	errand wait                  return once nothing holds it and nothing is queued for it
//	errand hold [NEEDS] [-n L]   a claim held while stdin is open: prints `granted <cards>`, reads `warm` / `busy`,
//	                                prints `evict`
//	errand require [NEEDS] [--extra] [--ranks N] [--entry NAME]   inside a run: its claim grown, waited for; prints
//	                                what to set. Exit 75: stopped to be run again with that up front ( runinfo/asks.go )
//
// NEEDS: --gpus N  --gpu-ram SIZE|SHARE  --ram SIZE  --cpus N  -x ( the machine to itself )

package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/queue"
	"github.com/errand-run/errand/internal/runinfo"
)

func interrupted() context.Context {
	ctx, _ := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	return ctx
}

func complain(verb string, err error) int {
	fmt.Fprintf(os.Stderr, "errand %s: %v\n", verb, err)
	return 2
}

func QueueMain(verb string, args []string) int {
	q := queue.Open()
	say := func(s string) { fmt.Fprintln(os.Stderr, strings.TrimSpace(s)) }
	switch verb {
	case "queue":
		fmt.Printf("\nThe machine (%s)\n", q.Host)
		fmt.Printf("  has  cpus=%g  ram=%.0fM  gpus=%d\n", q.Cap.CPUs, q.Cap.RAM, q.Cap.GPUs)
		if s := queue.Scheduler(); s != "" {
			fmt.Printf("  %s already decides what runs here; errand does not queue\n", s)
			return 0
		}
		lines := q.Describe()
		if len(lines) == 0 {
			fmt.Println("  nothing held")
		}
		for _, l := range lines {
			fmt.Println("  " + l)
		}
		fmt.Println("  " + q.Dir)
		return 0
	case "wait":
		if q.WaitIdle(interrupted()) != nil {
			return 130
		}
		return 0
	case "hold":
		needs, exclusive, rest, err := kit.NeedsFrom(args)
		label := ""
		for i := 0; err == nil && i < len(rest); i++ {
			if (rest[i] == "-n" || rest[i] == "--name") && i+1 < len(rest) {
				label = rest[i+1]
				i++
			} else {
				err = fmt.Errorf("%s: not something a claim says", rest[i])
			}
		}
		if err != nil {
			return complain(verb, err)
		}
		ctx := interrupted()
		g, err := q.Acquire(ctx, queue.Request{Needs: needs.Normalized(1), Exclusive: exclusive, Label: label, Say: say})
		if err != nil {
			return 130
		}
		defer g.Release()
		cards := make([]string, len(g.Devices))
		for i, d := range g.Devices {
			cards[i] = strconv.Itoa(d)
		}
		fmt.Println("granted " + strings.Join(cards, ","))
		words := make(chan string)
		go func() { // goes, and gives the machine back, when its input closes
			in := bufio.NewScanner(os.Stdin)
			for in.Scan() {
				words <- strings.TrimSpace(in.Text())
			}
			close(words)
		}()
		said := false
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return 130
			case w, open := <-words:
				if !open {
					return 0
				}
				if w == "warm" || w == "busy" {
					g.SetPreemptible(w == "warm")
					said = false
				}
			case <-tick.C:
			}
			if !said && g.Evicted() {
				fmt.Println("evict")
				said = true
			}
		}
	case "require":
		needs, exclusive, rest, err := kit.NeedsFrom(args)
		extra, ranks, entry := false, 0, ""
		for i := 0; err == nil && i < len(rest); i++ {
			name, value, glued := strings.Cut(rest[i], "=")
			take := func() string {
				if glued {
					return value
				}
				if i+1 < len(rest) {
					i++
					return rest[i]
				}
				err = fmt.Errorf("%s: a value is missing", rest[i])
				return ""
			}
			switch name {
			case "--extra":
				extra = true
			case "--ranks":
				if ranks, err = strconv.Atoi(take()); err == nil && ranks < 1 {
					err = fmt.Errorf("--ranks: a number of processes, 1 or more")
				}
			case "--entry":
				entry = take()
			default:
				err = fmt.Errorf("%s: not something a run requires", rest[i])
			}
		}
		if err != nil {
			return complain(verb, err)
		}
		// A run that errand can run again says so by the file it hands it: what cannot be given where the run is stops
		// it, and is written down with who asked ( runinfo/asks.go ). Without that file -- by hand, or under another errand --
		// nothing is stopped, and what could not be given is not waited for either.
		asks := os.Getenv(runinfo.EnvAsks)
		stop := func(a runinfo.Ask, why string) int {
			a.Entry, a.Needs, a.Exclusive, a.Extra, a.Why = runinfo.AskerOf(entry), needs, exclusive, extra, why
			if err := kit.AppendLine(asks, a); err != nil {
				return complain(verb, fmt.Errorf("what is asked cannot be written down: %v", err))
			}
			fmt.Fprintf(os.Stderr, "errand require: %s: stopped, to be run again with what it asks for\n", why)
			return 75
		}
		if asks != "" && ranks > 0 {
			have, _ := strconv.Atoi(os.Getenv(runinfo.EnvRanks))
			if have = max(have, 1); ranks > have {
				return stop(runinfo.Ask{Ranks: ranks}, fmt.Sprintf("%d ranks asked, this run has %d", ranks, have))
			}
		}
		if asks != "" && (os.Getenv(queue.EnvAllocation) != "" || queue.Scheduler() != "") && !extra {
			// inside an allocation what is asked is what the allocation has, or it is another allocation's
			switch have := q.Cap; {
			case needs.GPUs > have.GPUs:
				return stop(runinfo.Ask{}, fmt.Sprintf("%d gpus asked, the allocation has %d", needs.GPUs, have.GPUs))
			case have.CPUs > 0 && needs.CPUs > have.CPUs:
				return stop(runinfo.Ask{}, fmt.Sprintf("%g cpus asked, the allocation has %g", needs.CPUs, have.CPUs))
			case have.RAM > 0 && needs.RAM > have.RAM:
				return stop(runinfo.Ask{}, fmt.Sprintf("%.0f MB of ram asked, the allocation has %.0f", needs.RAM, have.RAM))
			}
		}
		path := os.Getenv(queue.EnvClaimFile)
		if path == "" {
			return 0 // not under a claim of errand's ( by hand, --no-queue, somebody's allocation ): nothing to grow
		}
		got, err := q.Grow(interrupted(), path, queue.Growth{Needs: needs, Exclusive: exclusive, Extra: extra, Say: say})
		if errors.Is(err, queue.ErrRerun) {
			say(err.Error())
			if asks != "" { // asked of the code, and stopped by it: run again with it up front
				return stop(runinfo.Ask{}, err.Error())
			}
			return 75
		}
		if err != nil {
			return complain(verb, err)
		}
		if len(got) > 0 {
			cards := make([]string, len(got))
			for i, d := range got {
				cards[i] = strconv.Itoa(d)
			}
			listed := strings.Join(cards, ",")
			for _, name := range []string{"CUDA_VISIBLE_DEVICES", "HIP_VISIBLE_DEVICES", "ROCR_VISIBLE_DEVICES", queue.EnvClaim} {
				fmt.Printf("%s=%s\n", name, listed)
			}
		}
		return 0
	}
	return 2
}
