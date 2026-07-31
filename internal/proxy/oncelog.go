package proxy

import (
	"fmt"
	"log"
	"sync"
)

// onceLogger reports a message only when it differs from the last one it
// reported. Both users are faults that recur per request rather than once: a
// response the injector cannot splice, and an upstream that will refuse every
// request until it comes back. Logging each occurrence buries the first line,
// which is the one that says what happened.
type onceLogger struct {
	logger *log.Logger

	mu   sync.Mutex
	last string
}

func newOnceLogger(logger *log.Logger) *onceLogger {
	return &onceLogger{logger: logger}
}

// log reports format, suppressing an identical repeat.
func (o *onceLogger) log(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	o.emit(msg, msg)
}

// logCause reports format, suppressing a repeat of the same cause. The cause is
// keyed separately from the message because whether a fault is a repeat depends
// on what went wrong, not on which request happened to hit it.
func (o *onceLogger) logCause(cause error, format string, args ...any) {
	o.emit(cause.Error(), fmt.Sprintf(format, args...))
}

// forget clears the last message, so the next occurrence is reported again. A
// success is what earns a fault the right to be logged a second time.
func (o *onceLogger) forget() {
	o.mu.Lock()
	o.last = ""
	o.mu.Unlock()
}

func (o *onceLogger) emit(key, msg string) {
	o.mu.Lock()
	repeat := key == o.last
	o.last = key
	o.mu.Unlock()
	if repeat {
		return
	}
	o.logger.Printf("marquee: %s", msg)
}
