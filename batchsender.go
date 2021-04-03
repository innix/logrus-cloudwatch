package logruscloudwatch

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

const (
	// maxSendAttempts is the maximum number of attempts to deliver the log events to CloudWatch.
	maxSendAttempts = 5

	// retryInterval is the duration to wait after a failed delivery before trying again.
	retryInterval = time.Second * 3

	// bufferCapacity is the number of log events that can be stored in the buffer.
	bufferCapacity = 100_000
)

// A batchSender buffers log events and sends them to CloudWatch in batches at fixed intervals.
type batchSender struct {
	// maxBatch is the maximum number of log events that can be sent to CloudWatch in one batch.
	maxBatch int

	// sleeper is the function for sleeping the current goroutine. This is normally the time.After method
	// but can be swapped for a different implementation to make testing easier.
	sleeper sleeper

	// interval is how long to sleep after sending a batch of log events to CloudWatch.
	interval time.Duration

	ch     chan types.InputLogEvent
	writer *cloudWatchLogWriter
	done   chan struct{}
}

// newBatchSender creates a new batchSender.
func newBatchSender(w *cloudWatchLogWriter, maxBatchSize int, interval time.Duration) *batchSender {
	return &batchSender{
		maxBatch: maxBatchSize,

		interval: interval,
		sleeper:  timeSleep,

		ch:     make(chan types.InputLogEvent, bufferCapacity),
		writer: w,
		done:   make(chan struct{}),
	}
}

// Run reads event logs from the buffer and sends them to CloudWatch in batches.
func (b *batchSender) Run(ctx context.Context) {
	defer close(b.done)

	for b.sleeper(ctx, b.interval) {
		msgs, done := b.getBatch(ctx, b.maxBatch)

		// Channel has been closed and drained.
		if done {
			break
		}

		if len(msgs) == 0 {
			continue
		}

		if err := b.sendBatchWithRetries(ctx, msgs); err != nil {
			// TODO: what to do with error?
			_ = err
		}
	}
}

// Stop will stop the batch sender from reading from the channel once it's empty. The channel
// reading is done in the Run method, so calling this method will cause the Run method to finish
// once the channel has been emptied.
//
// Stop doesn't wait for the batch sender to finish processing any remaining log events. To
// wait for that, use the Done method.
func (b *batchSender) Stop() {
	close(b.ch)

	// If the buffer can be finished off with 2 or less calls, remove the sleep interval
	// so it processes whatever is left without any sleeping.
	if len(b.ch) < (b.maxBatch * 2) { //nolint:gomnd // magic number `2` is explained in comment above.
		b.interval = 0
	}
}

// Done returns a channel that closes once the batch sender has been told to stop and has
// finished processing any remaining log events in the channel.
func (b *batchSender) Done() <-chan struct{} {
	return b.done
}

// getBatch reads upto max log events from the channel. It does not wait for events to arrive
// if the channel is empty; it will simply return an empty slice.
//
// If the context is canceled, the method returns a nil slice even if some messages had already
// been read from the channel.
//
// The method returns true if the channel has been closed and there's no log events left in the
// channel. It returns false in every other case.
func (b *batchSender) getBatch(ctx context.Context, max int) ([]types.InputLogEvent, bool) {
	var msgs []types.InputLogEvent
	var finished bool

	for i := 0; i < max && !finished; i++ {
		select {
		case msg, ok := <-b.ch:
			if !ok {
				return msgs, len(msgs) == 0
			}
			msgs = append(msgs, msg)

			break
		case <-ctx.Done():
			return nil, false
		default:
			finished = true
			break
		}
	}

	return msgs, false
}

// sendBatchWithRetries attempts to write the provided batch of log events to CloudWatch.
// The method will retry the write several times if not successful the first time.
// If the write is eventually successful, the method returns nil, even if multiple attempts
// were required. If the write failed on all attempts, the method returns the most recent
// error that occurred.
func (b *batchSender) sendBatchWithRetries(ctx context.Context, batch []types.InputLogEvent) error {
	var err error

	for i := 0; i < maxSendAttempts; i++ {
		err = b.writer.WriteToLogStream(ctx, batch)
		if err == nil {
			return nil
		}

		if !b.sleeper(ctx, retryInterval) {
			return err
		}
	}

	return err
}
