package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestUpdateCommandHelper(t *testing.T) {
	switch os.Getenv("PRINTMASTER_COMMAND_TEST") {
	case "hang":
		time.Sleep(time.Minute)
		os.Exit(0)
	case "output":
		fmt.Print(strings.Repeat("x", 128*1024))
		os.Exit(0)
	}
}

func TestUpdateCommandDeadlineAndOutputLimit(t *testing.T) {
	t.Parallel()
	m := &Manager{}
	start := time.Now()
	_, err := m.runUpdateCommand(100*time.Millisecond, []string{"PRINTMASTER_COMMAND_TEST=hang"}, os.Args[0], "-test.run=^TestUpdateCommandHelper$")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 10*time.Second {
		t.Fatalf("command was not bounded: %v (%s)", err, time.Since(start))
	}
	output, err := m.runUpdateCommand(10*time.Second, []string{"PRINTMASTER_COMMAND_TEST=output"}, os.Args[0], "-test.run=^TestUpdateCommandHelper$")
	if err != nil || len(output) != 64*1024 {
		t.Fatalf("output limit: bytes=%d err=%v", len(output), err)
	}
}
