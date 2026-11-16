package artisan

// interruptGraceful is whether Interrupt is a dependable graceful rung:
// CTRL_BREAK reaches only a worker sharing the console and may end it
// abruptly, so it is not.
const interruptGraceful = false
