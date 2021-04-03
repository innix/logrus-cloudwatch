# Logrus CloudWatch

[![Go Reference](https://pkg.go.dev/badge/github.com/innix/logrus-cloudwatch.svg)](https://pkg.go.dev/github.com/innix/logrus-cloudwatch)

A [logrus](https://github.com/sirupsen/logrus) hook for sending log events to [AWS CloudWatch](https://aws.amazon.com/cloudwatch/).

## Install

```bash
go get github.com/innix/logrus-cloudwatch
```

## Usage

This hook uses the [AWS SDK for Go V2](https://aws.github.io/aws-sdk-go-v2/). If your project is using the legacy
V1 SDK, you can still use this hook but it means your project will have a dependency on both versions of the SDK.

```go
package main

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/innix/logrus-cloudwatch"
	log "github.com/sirupsen/logrus"
)

func main() {
	// Setup AWS SDK client.
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("Could not load AWS config: %v", err)
	}
	client := cloudwatchlogs.NewFromConfig(cfg)

	// Create and register hook.
	hook, err := logruscloudwatch.New(client, nil)
	if err != nil {
		log.Fatalf("Could not create CloudWatch hook: %v", err)
	}
	log.AddHook(hook)

	// Do some logging.
	for i := 0; i < 10; i++ {
		log.WithField("counter", i).Info("Incremented counter.")
		time.Sleep(time.Second * 2)
	}

	// Ensure all logs are sent to CloudWatch before the program exits.
	<-hook.Stop()
}
```

## Specify log group name

The `Options` struct contains fields for configuring how the hook works, including which
CloudWatch log group to write to.

```go
// Create and register hook.
hook, err := logruscloudwatch.New(client, &logruscloudwatch.Options{
    LogGroupName: "/my-project/development",
})
if err != nil {
    log.Fatalf("Could not create CloudWatch hook: %v", err)
}
log.AddHook(hook)
```

## Test

To run the unit tests:

```bash
go test ./...
```

## Lint

The project uses [golangci-lint](https://golangci-lint.run/) for linting. See
the [.golangci.yml](.golangci.yml) file for configured rules. To run the linter:

```bash
golangci-lint run
```
