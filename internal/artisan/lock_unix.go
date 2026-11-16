//go:build unix

package artisan

// interruptGraceful is whether Interrupt is a dependable graceful rung:
// SIGINT reaches the whole group.
const interruptGraceful = true
