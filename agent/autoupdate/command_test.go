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
	case "directory":
		dir, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		fmt.Print(dir)
		os.Exit(0)
	}
}

func TestUpdateCommandUsesWorkspaceAndParentCancellation(t *testing.T) {
	t.Parallel()
	m := &Manager{}
	dir := t.TempDir()
	output, err := m.runUpdateCommandIn(context.Background(), 10*time.Second, dir,
		[]string{"PRINTMASTER_COMMAND_TEST=directory"}, os.Args[0], "-test.run=^TestUpdateCommandHelper$")
	if err != nil || !strings.EqualFold(string(output), dir) {
		t.Fatalf("command workspace: %q %v", output, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = m.runUpdateCommandIn(ctx, time.Minute, dir,
		[]string{"PRINTMASTER_COMMAND_TEST=hang"}, os.Args[0], "-test.run=^TestUpdateCommandHelper$")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("parent cancellation lost: %v", err)
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
