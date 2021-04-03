package logruscloudwatch

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

// cloudWatchMaxLogEvents is the maximum number of log events that can be sent to CloudWatch
// in one batch.
const cloudWatchMaxLogEvents = 10_000

// cloudWatchLogWriter writes log events to CloudWatch. It automatically handles creating the
// log group and log stream if they don't exist. The creation is done lazily, so they are only
// created once a log event needs to be written.
type cloudWatchLogWriter struct {
	// Client is the CloudWatch client used to make API requests.
	Client *cloudwatchlogs.Client

	// GroupName is the log group name to write log events to.
	GroupName string

	// StreamName is the log stream name to write log events to.
	StreamName string

	nextToken *string
	inited    bool
	mu        sync.RWMutex
}

// WriteToLogStream writes the provided log events to CloudWatch. It sends them all at once
// as a batch request.
func (w *cloudWatchLogWriter) WriteToLogStream(ctx context.Context, evs []types.InputLogEvent) error {
	if err := w.initialize(ctx); err != nil {
		return err
	}

	resp, err := w.Client.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
		LogEvents:     evs,
		LogGroupName:  aws.String(w.GroupName),
		LogStreamName: aws.String(w.StreamName),
		SequenceToken: w.nextToken,
	})
	if err != nil {
		return fmt.Errorf("logruscloudwatch: could not write log events to CloudWatch: %w", err)
	}

	w.nextToken = resp.NextSequenceToken
	return nil
}

// initialize ensures that the log group and log stream exist, and creates them if they don't.
func (w *cloudWatchLogWriter) initialize(ctx context.Context) error {
	if w.isInit() {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.inited {
		return nil
	}

	if err := w.createLogGroupIfNotExists(ctx); err != nil {
		return err
	}
	if err := w.createLogStreamIfNotExists(ctx); err != nil {
		return err
	}
	token, err := w.fetchUploadToken(ctx)
	if err != nil {
		return err
	}

	w.nextToken = token
	w.inited = true
	return nil
}

func (w *cloudWatchLogWriter) createLogGroupIfNotExists(ctx context.Context) error {
	_, err := w.Client.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName: aws.String(w.GroupName),
		Limit:        aws.Int32(1),
	})
	if err == nil {
		return nil
	}

	var rnferr *types.ResourceNotFoundException
	if !errors.As(err, &rnferr) {
		return fmt.Errorf("logruscloudwatch: could not check if log group exists on CloudWatch: %w", err)
	}

	_, err = w.Client.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{
		LogGroupName: aws.String(w.GroupName),
	})
	if err != nil {
		return fmt.Errorf("logruscloudwatch: could not create log group on CloudWatch: %w", err)
	}

	return nil
}

func (w *cloudWatchLogWriter) createLogStreamIfNotExists(ctx context.Context) error {
	_, err := w.Client.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{
		LogGroupName:  aws.String(w.GroupName),
		LogStreamName: aws.String(w.StreamName),
	})
	if err == nil {
		return nil
	}

	var raeerr *types.ResourceAlreadyExistsException
	if !errors.As(err, &raeerr) {
		return fmt.Errorf("logruscloudwatch: could not create log stream on CloudWatch: %w", err)
	}

	return nil
}

func (w *cloudWatchLogWriter) fetchUploadToken(ctx context.Context) (*string, error) {
	resp, err := w.Client.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName:        aws.String(w.GroupName),
		LogStreamNamePrefix: aws.String(w.StreamName),
	})
	if err != nil {
		return nil, fmt.Errorf("logruscloudwatch: could not fetch prefixed log streams from CloudWatch: %w", err)
	}

	for _, s := range resp.LogStreams {
		if aws.ToString(s.LogStreamName) == w.StreamName {
			return s.UploadSequenceToken, nil
		}
	}

	return nil, errors.New("logruscloudwatch: log stream with exact name not found")
}

func (w *cloudWatchLogWriter) isInit() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()

	return w.inited
}
