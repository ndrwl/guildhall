package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// stewardSignals are the signals that interrupt a Steward command: Ctrl-C and a polite kill.
var stewardSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

// interruptContext is parent cancelled by the first of sig, through a.AdvanceContext or else signal.NotifyContext.
// The subscription ends when the context does, so a second signal gets its default action and ends the process.
func (a *App) interruptContext(parent context.Context, sig ...os.Signal) (context.Context, context.CancelFunc) {
	notify := a.AdvanceContext
	if notify == nil {
		notify = signal.NotifyContext
	}
	ctx, stop := notify(parent, sig...)
	context.AfterFunc(ctx, stop)
	return ctx, stop
}

// stewardContext is the context of a Steward command: cancelled when it is interrupted, so the scp and ssh it runs
// are stopped, their process groups killed, before it records the outcome and releases its lock.
func (a *App) stewardContext() (context.Context, context.CancelFunc) {
	return a.interruptContext(context.Background(), stewardSignals...)
}
