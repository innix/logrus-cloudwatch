package logruscloudwatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	uuid "github.com/satori/go.uuid"
	"github.com/sirupsen/logrus"
)

const (
	defaultMaxBatchSize = 500

	// batchSendFrequency is how often to send a batch of events to CloudWatch. The PutLogEvents
	// API call is throttled to 5 requests per second, so the absolute minimum frequency that can
	// be used is 200ms.
	batchSendFrequency = time.Millisecond * 500
)

// ErrStopped is raised when attempting to use the hook after if has been stopped.
var ErrStopped = errors.New("logruscloudwatch: hook has been stopped")

var (
	defaultLogLevels = logrus.AllLevels
	defaultFormatter = &LogFormatter{
		FieldMap: FieldMap{
			logrus.FieldKeyMsg:  "message",
			logrus.FieldKeyTime: "datetime",
		},
		DataKey: "fields",
	}
)

// Hook is a logrus.Hook that sends log events to AWS CloudWatch.
type Hook struct {
	lvls   []logrus.Level
	fmtr   logrus.Formatter
	sender *batchSender

	stopped        bool
	errWhenStopped bool
	mu             sync.RWMutex
}

// Options is the struct used to configure the hook.
type Options struct {

	// Levels is the log levels to send to CloudWatch. If not set, all log levels are sent.
	Levels []logrus.Level

	// Formatter is the log formatter used to serialize the log entry before sending it to
	// CloudWatch. If not set, the default formatter will be used, which is a LogFormatter.
	Formatter logrus.Formatter

	// MaxBatchSize is the maximum number of log events to send to CloudWatch in one batch.
	// If not set, the default value of 500 will be used. The maximum allowed value is 10,000.
	MaxBatchSize int

	// ReturnErrorIfStopped is a flag that determines if the Hook.Fire method returns ErrStopped
	// if it's called after the hook has been stopped. If set to false, the Hook.Fire method just
	// returns nil. The default value is false.
	ReturnErrorIfStopped bool

	// LogGroupName is the name of the CloudWatch log group to write to. If not set, one will
	// be automatically generated. The auto-generated name will be the name of the running binary
	// as returned by os.Executable.
	LogGroupName string

	// LogStreamName is the name of the CloudWatch log stream to write to. If not set, one will
	// be automatically generated. The auto-generated name will consist of the local current
	// datetime, the computer name, and a v4 UUID.
	//
	// Here is an example of an auto-generated stream name:
	//     2021-05-19-17-49-05_INNIX-HOME-LAPTOP_cbd2917a-6d77-4bae-8069-7f38c3034ed3
	LogStreamName string
}

// New creates a new Logrus hook that sends log events to AWS CloudWatch. If the Options value
// is nil then the default values will be used. See the Options struct for more information on
// default values.
func New(client *cloudwatchlogs.Client, o *Options) (*Hook, error) {
	if client == nil {
		return nil, errors.New("logruscloudwatch: client is nil")
	}

	var opts Options
	if o != nil {
		opts = *o
	}

	if opts.Levels == nil {
		opts.Levels = defaultLogLevels
	}

	if opts.Formatter == nil {
		opts.Formatter = defaultFormatter
	}

	switch {
	case opts.MaxBatchSize > cloudWatchMaxLogEvents:
		return nil, fmt.Errorf("logruscloudwatch: max batch size exceeds limit (%d > %d)", opts.MaxBatchSize, cloudWatchMaxLogEvents)
	case opts.MaxBatchSize < 0:
		return nil, fmt.Errorf("logruscloudwatch: max batch size must be non-negative: %d < 0", opts.MaxBatchSize)
	case opts.MaxBatchSize == 0:
		opts.MaxBatchSize = defaultMaxBatchSize
	}

	if opts.LogGroupName == "" {
		gn, err := newLogGroupName()
		if err != nil {
			return nil, fmt.Errorf("logruscloudwatch: could not generate log group name: %w", err)
		}
		opts.LogGroupName = gn
	}

	if opts.LogStreamName == "" {
		sn, err := newLogStreamName()
		if err != nil {
			return nil, fmt.Errorf("logruscloudwatch: could not generate log stream name: %w", err)
		}
		opts.LogStreamName = sn
	}

	writer := &cloudWatchLogWriter{
		Client:     client,
		GroupName:  opts.LogGroupName,
		StreamName: opts.LogStreamName,
	}

	sender := newBatchSender(writer, opts.MaxBatchSize, batchSendFrequency)
	go sender.Run(context.Background())

	h := &Hook{
		lvls:           opts.Levels,
		fmtr:           opts.Formatter,
		errWhenStopped: opts.ReturnErrorIfStopped,
		sender:         sender,
	}

	return h, nil
}

// Fire takes a log entry and sends it to CloudWatch. Log entries are sent in batches at fixed intervals,
// so they might not be immediately sent to CloudWatch when this method is called.
func (h *Hook) Fire(e *logrus.Entry) error {
	if h.isStopped() {
		if h.errWhenStopped {
			return ErrStopped
		}
		return nil
	}

	msg, err := h.fmtr.Format(e)
	if err != nil {
		return fmt.Errorf("logruscloudwatch: could not format log entry: %w", err)
	}

	ev := types.InputLogEvent{
		Message:   aws.String(string(msg)),
		Timestamp: aws.Int64(int64(time.Nanosecond) * e.Time.UnixNano() / int64(time.Millisecond)),
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	// Check again inside the mutex because the state may have changed since the first check.
	if h.stopped {
		if h.errWhenStopped {
			return ErrStopped
		}
		return nil
	}

	// Don't wait if buffer is full. Return an immediate error instead.
	select {
	case h.sender.ch <- ev:
		return nil
	default:
		return errors.New("logruscloudwatch: buffer is full")
	}
}

// Levels defines the level of log events that will be sent to CloudWatch. Any log events that
// is not one of these levels is simply discarded.
func (h *Hook) Levels() []logrus.Level {
	return h.lvls
}

// Stop stops the hook from accepting new log entries and begins a graceful shutdown. It is
// important to call this method once you're finished using the hook because it releases a
// background goroutine.
//
// It is recommended to call this method at the end of your program's shutdown procedure, when
// no further logging will occur. If the hook is stopped but is still registered to a logger,
// any log entries the hook receives will be ignored. If the Options.ReturnErrorIfStopped field
// was set to true, then the Fire method will return an ErrStopped error. If set to false, it
// returns nil.
//
// Stop returns a channel that closes when all remaining buffered log events have been sent to
// CloudWatch. If it's essential that all log entries are uploaded, call this method when your
// program is shutting down and wait for the channel to close.
func (h *Hook) Stop() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.stopped {
		h.sender.Stop()
		h.stopped = true
	}

	return h.sender.Done()
}

func (h *Hook) isStopped() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return h.stopped
}

func newLogGroupName() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("could not get executable path: %w", err)
	}

	name := filepath.Base(exe)
	if ext := filepath.Ext(name); ext != "" {
		name = strings.TrimSuffix(name, ext)
	}

	return name, nil
}

func newLogStreamName() (string, error) {
	hn, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("could not get computer host name: %w", err)
	}

	tm := time.Now().Format("2006-01-02-15-04-05")
	id := uuid.NewV4()

	return fmt.Sprintf("%s_%s_%s", tm, hn, id), nil
}
