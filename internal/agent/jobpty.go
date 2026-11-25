// A run's terminal, as its place's agent gives it ( ADR 0005: a process has a terminal when its client has one;
// ADR 0016: how the streams are kept apart ). Each of standard output and standard error is a pseudo-terminal of its
// own, so that what each wrote is still known -- the gutter of whoever draws, `stderr.txt` -- while both are a
// terminal to the run: colours, progress bars, its width. What is written to them comes out as it was written, with no
// carriage return added. Standard input is the first of them when whoever typed the command reads what it types:
// the run's `input()` and `[ -t 0 ]` are then those of a person at a terminal, and what is typed is echoed and edited by
// the line discipline as it would be anywhere.
//
// Nothing of the run's depends on whoever follows it: the terminal is the job's, in the agent that holds the job; its
// size is the last one it was told; a window that changes arrives as a frame.

package agent

import (
	"os"

	"github.com/errand-run/errand/internal/draw"
	"github.com/errand-run/errand/internal/runinfo"
)

type jobPty struct {
	out, err           *os.File // the agent's ends: what the run writes is read there, what is typed is written to the first
	outSlave, errSlave *os.File // the run's
}

func openJobPty(t *runinfo.TtySpec) (*jobPty, error) {
	p := &jobPty{}
	var err error
	if p.out, p.outSlave, err = draw.OpenPty(); err != nil {
		return nil, err
	}
	if t.One { // a shell: one terminal for all three, as any terminal is
		p.err, p.errSlave = p.out, p.outSlave
		return p, nil
	}
	if p.err, p.errSlave, err = draw.OpenPty(); err != nil {
		p.out.Close()
		p.outSlave.Close()
		return nil, err
	}
	draw.AsWritten(p.outSlave)
	draw.AsWritten(p.errSlave)
	return p, nil
}

func (p *jobPty) resize(cols, rows int) {
	if cols > 0 && rows > 0 {
		draw.SetWinsize(p.out, cols, rows)
		draw.SetWinsize(p.err, cols, rows)
	}
}

// ptyInput: what is typed to a run goes in at the terminal's master; its end is the end-of-file character, which a
// terminal reads as the end of a line's input
type ptyInput struct{ *os.File }

func (p ptyInput) Close() error {
	_, err := p.File.Write([]byte{4})
	return err
}

// hasTty: whether this job has a terminal -- known before its process is, since the follower asks as soon as the job
// exists
func (j *job) hasTty() bool { return j.spec.Tty != nil && !j.spec.Batch }

// resize: whoever follows the job changed the size of its window. A job not started yet takes it when it is.
func (j *job) resize(cols, rows int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if cols > 0 && rows > 0 {
		j.cols, j.rows = cols, rows
	}
	if j.pty != nil {
		j.pty.resize(cols, rows)
	}
}
