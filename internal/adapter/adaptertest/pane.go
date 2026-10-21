package adaptertest

import (
	"context"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
)

// StartInPane returns a Subject.Start that runs the runtime the way
// `artisan start --herdr` does: l types the command into a new tab, quoted for
// the tab's shell, and a stop is Ctrl+C in that tab. The runtime has exited
// once the tab's shell is again its only foreground process.
func StartInPane(l adapter.TabLauncher) func(t *testing.T, spec adapter.Spec, cmd adapter.Command) (Process, error) {
	return func(t *testing.T, spec adapter.Spec, cmd adapter.Command) (Process, error) {
		run, err := l.Start(context.Background(), "artisan "+spec.TaskID+"/"+spec.AttemptID, cmd)
		if err != nil {
			return nil, err
		}
		p := &paneProcess{run: run, exited: make(chan struct{})}
		go func() {
			defer close(p.exited)
			for {
				info, err := l.Host.PaneProcessInfo(context.Background(), run.Tab)
				if err != nil {
					t.Errorf("pane %s: %v", run.Tab.PaneID, err)
					return
				}
				if info.ShellIdle() {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
		return p, nil
	}
}

// paneProcess is a runtime running in a tab.
type paneProcess struct {
	run    *adapter.TabRun
	exited chan struct{}
}

func (p *paneProcess) Exited() <-chan struct{} { return p.exited }

// Stop sends Ctrl+C to the tab; at an idle shell prompt that does nothing.
func (p *paneProcess) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return p.run.Stop(ctx)
}
