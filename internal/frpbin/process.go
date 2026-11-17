package frpbin

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	logLines    = 500
	stopTimeout = 5 * time.Second
	maxBackoff  = 30 * time.Second
)

// Process runs one frp binary as a child process, restarting it when it
// exits and keeping its recent output.
type Process struct {
	name   string
	bin    string
	args   []string
	log    *slog.Logger
	logs   *LogBuffer
	reload chan struct{}

	mu      sync.Mutex
	running bool
}

// NewProcess builds a Process; call Run to start it.
func NewProcess(name, bin string, log *slog.Logger, args ...string) *Process {
	return &Process{name: name, bin: bin, args: args, log: log.With("proc", name),
		logs: NewLogBuffer(logLines), reload: make(chan struct{}, 1)}
}

// Logs returns the process's recent output lines.
func (p *Process) Logs() []string { return p.logs.Lines() }

// Running reports whether the child is currently up.
func (p *Process) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

// Restart asks Run to stop the child and start it again (e.g. after its
// config file changed). It does not block.
func (p *Process) Restart() {
	select {
	case p.reload <- struct{}{}:
	default:
	}
}

// Run keeps the child running until ctx is done.
func (p *Process) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		restarted := p.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if restarted {
			backoff = time.Second
			continue
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		p.log.Warn("process exited, restarting", "in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-p.reload:
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// runOnce runs the child until it exits, ctx ends, or a restart is requested
// (then it returns true).
func (p *Process) runOnce(ctx context.Context) (restarted bool) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(runCtx, p.bin, p.args...) //nolint:gosec // our own pinned binary
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = stopTimeout
	killWithParent(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		p.log.Error("pipe", "err", err)
		return false
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		p.log.Error("start failed", "err", err)
		p.logs.Add(fmt.Sprintf("frpdash: start %s: %v", p.name, err))
		return false
	}
	p.setRunning(true)
	defer p.setRunning(false)
	p.log.Info("started", "pid", cmd.Process.Pid)

	done := make(chan struct{})
	go func() {
		p.pump(out)
		close(done)
	}()
	reloaded := make(chan bool, 1)
	go func() {
		select {
		case <-p.reload:
			cancel()
			reloaded <- true
		case <-runCtx.Done():
			reloaded <- false
		}
	}()

	<-done
	err = cmd.Wait()
	cancel()
	restarted = <-reloaded
	if !restarted && ctx.Err() == nil {
		p.log.Warn("exited", "err", err)
	}
	return restarted
}

func (p *Process) pump(r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		p.logs.Add(line)
		p.log.Info(line)
	}
}

func (p *Process) setRunning(v bool) {
	p.mu.Lock()
	p.running = v
	p.mu.Unlock()
}

// Verify runs "<bin> verify -c cfg" and returns frp's message on failure.
func Verify(ctx context.Context, bin, cfg string) error {
	out, err := exec.CommandContext(ctx, bin, "verify", "-c", cfg).CombinedOutput() //nolint:gosec // pinned binary
	if err != nil {
		return fmt.Errorf("frp rejected config: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// WriteFileAtomic writes data via a temp file and rename, with mode perm.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LogBuffer keeps the last N lines.
type LogBuffer struct {
	mu    sync.Mutex
	lines []string
	next  int
	full  bool
}

// NewLogBuffer makes a buffer holding n lines.
func NewLogBuffer(n int) *LogBuffer { return &LogBuffer{lines: make([]string, n)} }

// Add appends a line, dropping the oldest when full.
func (b *LogBuffer) Add(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines[b.next] = line
	b.next = (b.next + 1) % len(b.lines)
	if b.next == 0 {
		b.full = true
	}
}

// Lines returns the buffered lines, oldest first.
func (b *LogBuffer) Lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.full {
		return append([]string(nil), b.lines[:b.next]...)
	}
	return append(append([]string(nil), b.lines[b.next:]...), b.lines[:b.next]...)
}
