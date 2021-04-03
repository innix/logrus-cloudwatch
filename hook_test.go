package logruscloudwatch

import (
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/sirupsen/logrus"
)

func TestNewWithNilClient(t *testing.T) {
	t.Parallel()

	if _, err := New(nil, &Options{}); err == nil {
		t.Error("expected non-nil error, got nil error.")
	}
}

func TestNewWithNilOptions(t *testing.T) {
	t.Parallel()

	hook, err := New(&cloudwatchlogs.Client{}, nil)
	if err != nil {
		t.Fatalf("expected nil error, got non-nil error: %v", err)
	}
	defer hook.Stop()

	sender := hook.sender
	writer := sender.writer

	if !reflect.DeepEqual(hook.lvls, defaultLogLevels) {
		t.Errorf("expected log levels to be the default, set to %v instead.", hook.lvls)
	}

	if hook.fmtr != defaultFormatter {
		t.Errorf("expected formatter to be the default, set to %v (%T) instead.", hook.fmtr, hook.fmtr)
	}

	if sender.maxBatch != defaultMaxBatchSize {
		t.Errorf("expected max batch size to be the default, set to %v instead.", sender.maxBatch)
	}

	if writer.GroupName == "" {
		t.Error("expected log group name to be non-empty, but it is empty")
	}

	if writer.StreamName == "" {
		t.Error("expected log stream name to be non-empty, but it is empty")
	}
}

func TestNewWithMaxBatchSize(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		Name                 string
		MaxBatchSize         int
		ExpectedMaxBatchSize int
		ExpectError          bool
	}{
		// Good values.
		{Name: "good 1", MaxBatchSize: 0, ExpectedMaxBatchSize: defaultMaxBatchSize},
		{Name: "good 2", MaxBatchSize: 100, ExpectedMaxBatchSize: 100},
		{Name: "good 3", MaxBatchSize: cloudWatchMaxLogEvents, ExpectedMaxBatchSize: cloudWatchMaxLogEvents},
		{Name: "good 4", MaxBatchSize: cloudWatchMaxLogEvents - 1, ExpectedMaxBatchSize: cloudWatchMaxLogEvents - 1},

		// Bad values.
		{Name: "bad 1", MaxBatchSize: cloudWatchMaxLogEvents + 1, ExpectError: true},
		{Name: "bad 2", MaxBatchSize: 100_000, ExpectError: true},
		{Name: "bad 3", MaxBatchSize: -1, ExpectError: true},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			hook, err := New(&cloudwatchlogs.Client{}, &Options{MaxBatchSize: tc.MaxBatchSize})
			if err != nil {
				if !tc.ExpectError {
					t.Errorf("expected nil error, got non-nil error: %v", err)
				}

				return
			}
			defer hook.Stop()

			if tc.ExpectError {
				t.Error("expected non-nil error, got nil error.")
			}

			sender := hook.sender
			if sender.maxBatch != tc.ExpectedMaxBatchSize {
				t.Errorf("hook max batch size: expected %v, got %v instead.", tc.ExpectedMaxBatchSize, sender.maxBatch)
			}
		})
	}
}

func TestHook_Levels(t *testing.T) {
	t.Parallel()

	dbgLvl := []logrus.Level{logrus.DebugLevel}
	dbgInfoLvl := []logrus.Level{logrus.DebugLevel, logrus.InfoLevel}
	dbgInfoWarnLvl := []logrus.Level{logrus.DebugLevel, logrus.InfoLevel, logrus.WarnLevel}

	testCases := []struct {
		Name           string
		Opts           *Options
		ExpectedLevels []logrus.Level
	}{
		{Name: "nil", Opts: nil, ExpectedLevels: defaultLogLevels},
		{Name: "empty", Opts: &Options{}, ExpectedLevels: defaultLogLevels},
		{Name: "dbg", Opts: &Options{Levels: dbgLvl}, ExpectedLevels: dbgLvl},
		{Name: "dbgInfo", Opts: &Options{Levels: dbgInfoLvl}, ExpectedLevels: dbgInfoLvl},
		{Name: "dbgInfoWarn", Opts: &Options{Levels: dbgInfoWarnLvl}, ExpectedLevels: dbgInfoWarnLvl},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			hook, err := New(&cloudwatchlogs.Client{}, tc.Opts)
			if err != nil {
				t.Fatalf("expected nil error, got non-nil error: %v", err)
			}
			defer hook.Stop()

			if !reflect.DeepEqual(hook.Levels(), tc.ExpectedLevels) {
				t.Errorf("hook log levels: expected %v, got %v", tc.ExpectedLevels, hook.Levels())
			}
		})
	}
}

func TestHook_Fire(t *testing.T) {
	t.Parallel()

	hook, err := New(&cloudwatchlogs.Client{}, nil)
	if err != nil {
		t.Fatalf("expected nil error, got non-nil error: %v", err)
	}
	defer hook.Stop()

	sender := hook.sender
	ch := make(chan types.InputLogEvent, 1)
	sender.ch = ch
	sender.interval = time.Millisecond * 10

	e := nullLogEntry()
	e.Info("test")

	if err := hook.Fire(e); err != nil {
		t.Errorf("expected nil error, got non-nil error: %v", err)
		return
	}

	// Give some time for message to receive, because batcher runs at intervals.
	select {
	case <-ch:
		break
	case <-time.After(time.Second * 3):
		t.Error("hook did not write to channel")
		break
	}
}

func TestHook_FireWhenClosed(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		Name           string
		Opts           *Options
		ExpectingError bool
		Err            error
	}{
		{Name: "nil opts", Opts: nil, Err: nil, ExpectingError: false},
		{Name: "no return err", Opts: &Options{ReturnErrorIfStopped: false}, Err: nil, ExpectingError: false},
		{Name: "return err", Opts: &Options{ReturnErrorIfStopped: true}, Err: ErrStopped, ExpectingError: true},
	}

	e := nullLogEntry()
	e.Info("test")

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			hook, err := New(&cloudwatchlogs.Client{}, tc.Opts)
			if err != nil {
				t.Fatalf("expected nil error, got non-nil error: %v", err)
			}
			hook.Stop()

			if err := hook.Fire(e); err != nil {
				if !tc.ExpectingError {
					t.Errorf("expected nil error, got %v", err)
					return
				}

				if !errors.Is(err, tc.Err) {
					t.Errorf("expected specific error %v, got %v", tc.Err, err)
				}
			} else if tc.ExpectingError {
				t.Error("expected non-nil error, got nil error")
			}
		})
	}
}

func nullLogEntry() *logrus.Entry {
	l := logrus.New()
	l.Out = io.Discard

	return logrus.NewEntry(l)
}
