package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestSetupServiceDirectories(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific test")
	}

	// Create a temporary ProgramData directory for testing
	tempDir := t.TempDir()
	originalProgramData := os.Getenv("ProgramData")
	os.Setenv("ProgramData", tempDir)
	defer os.Setenv("ProgramData", originalProgramData)

	err := setupServiceDirectories()
	if err != nil {
		t.Fatalf("setupServiceDirectories() failed: %v", err)
	}

	// Verify expected directories were created
	expectedDirs := []string{
		filepath.Join(tempDir, "PrintMaster"),
		filepath.Join(tempDir, "PrintMaster", "agent"),
		filepath.Join(tempDir, "PrintMaster", "agent", "logs"),
	}

	for _, dir := range expectedDirs {
		info, err := os.Stat(dir)
		if err != nil {
			t.Errorf("Directory not created: %s, error: %v", dir, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("Expected %s to be a directory", dir)
		}
	}
}

func TestGetServiceLogPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific test")
	}

	// Create a temporary ProgramData directory
	tempDir := t.TempDir()
	originalProgramData := os.Getenv("ProgramData")
	os.Setenv("ProgramData", tempDir)
	defer os.Setenv("ProgramData", originalProgramData)

	logPath := getServiceLogPath()
	expectedPath := filepath.Join(tempDir, "PrintMaster", "agent", "logs", "agent.log")

	if logPath != expectedPath {
		t.Errorf("getServiceLogPath() = %s, want %s", logPath, expectedPath)
	}

	// Verify the path contains the agent subdirectory
	if !filepath.IsAbs(logPath) {
		t.Errorf("Log path should be absolute: %s", logPath)
	}

	// Check that path contains "agent" subdirectory
	if !containsPathSegment(logPath, "agent") {
		t.Errorf("Log path should contain 'agent' subdirectory: %s", logPath)
	}
}

func TestWindowsServiceRegistersProtectedExecutable(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific test")
	}
	programFiles := t.TempDir()
	t.Setenv("ProgramFiles", programFiles)
	config := getServiceConfig()
	expected := filepath.Join(programFiles, "PrintMaster", "Agent", "printmaster-agent.exe")
	if config.Executable != expected {
		t.Fatalf("service executable = %q, want %q", config.Executable, expected)
	}
}

func TestSSEHubShutdown(t *testing.T) {
	t.Parallel()

	hub := NewSSEHub()

	// Create a client
	client := hub.NewClient()

	// Give the hub time to register the client
	time.Sleep(100 * time.Millisecond)

	// Send an event to verify the client is working
	testEvent := SSEEvent{Type: "test", Data: map[string]interface{}{"msg": "data"}}
	hub.Broadcast(testEvent)

	// Client should receive it
	select {
	case <-client.events:
		// Good, client is working
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Client did not receive test event - hub may not be working")
	}

	// Now stop the hub
	hub.Stop()

	// Give the hub time to shut down
	time.Sleep(100 * time.Millisecond)

	// Verify client channel is closed
	select {
	case _, ok := <-client.events:
		if ok {
			t.Error("Client event channel should be closed after shutdown")
		}
		// Successfully read closed channel
	case <-time.After(100 * time.Millisecond):
		t.Error("Client event channel should be closed and readable")
	}

	// Verify clients map is empty after shutdown
	hub.mu.RLock()
	clientCount := len(hub.clients)
	hub.mu.RUnlock()

	if clientCount != 0 {
		t.Errorf("Expected 0 clients after shutdown, got %d", clientCount)
	}
}

func TestSSEHubBroadcast(t *testing.T) {
	t.Parallel()

	hub := NewSSEHub()
	defer hub.Stop()

	client := hub.NewClient()

	// Give the hub time to register
	time.Sleep(10 * time.Millisecond)

	// Broadcast an event
	event := SSEEvent{
		Type: "test",
		Data: map[string]interface{}{"message": "hello"},
	}
	hub.Broadcast(event)

	// Client should receive the event
	select {
	case received := <-client.events:
		if received.Type != "test" {
			t.Errorf("Expected event type 'test', got '%s'", received.Type)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Client did not receive broadcast event")
	}
}

func TestBackgroundGoroutinesRespectContext(t *testing.T) {
	t.Parallel()

	// Test that background goroutines stop when context is cancelled
	// We cancel the context immediately so they never execute the work function
	testCases := []struct {
		name     string
		testFunc func(ctx context.Context, done chan struct{})
	}{
		{
			name: "metrics downsampler",
			testFunc: func(ctx context.Context, done chan struct{}) {
				go func() {
					runMetricsDownsampler(ctx, nil)
					close(done)
				}()
			},
		},
		{
			name: "garbage collection",
			testFunc: func(ctx context.Context, done chan struct{}) {
				go func() {
					runGarbageCollection(ctx, nil, nil)
					close(done)
				}()
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})

			// Start the goroutine
			tc.testFunc(ctx, done)

			// Cancel immediately - the goroutine should exit during startup delay
			cancel()

			// Goroutine should stop within a reasonable time
			select {
			case <-done:
				// Success - goroutine stopped
			case <-time.After(2 * time.Second):
				t.Errorf("%s goroutine did not stop after context cancellation", tc.name)
			}
		})
	}
}

func TestUploadWorkerShutdown(t *testing.T) {
	t.Parallel()

	// Create a mock logger
	mockLogger := &mockLogger{}

	// Create a mock upload worker with proper initialization
	worker := &UploadWorker{
		stopCh: make(chan struct{}),
		logger: mockLogger,
	}

	// Simulate worker running
	worker.wg.Add(1)
	go func() {
		defer worker.wg.Done()
		<-worker.stopCh
	}()

	// Stop the worker
	stopped := make(chan struct{})
	go func() {
		worker.Stop()
		close(stopped)
	}()

	// Worker should stop within reasonable time
	select {
	case <-stopped:
		// Success
	case <-time.After(2 * time.Second):
		t.Error("Upload worker did not stop in time")
	}
}

// mockLogger implements the Logger interface for testing
type mockLogger struct{}

func (m *mockLogger) Error(msg string, context ...interface{}) {}
func (m *mockLogger) Warn(msg string, context ...interface{})  {}
func (m *mockLogger) Info(msg string, context ...interface{})  {}
func (m *mockLogger) Debug(msg string, context ...interface{}) {}

// Helper function to check if a path contains a specific segment
func containsPathSegment(path, segment string) bool {
	// Simple string check - just verify "agent" appears in the path
	return filepath.Base(filepath.Dir(filepath.Dir(path))) == segment ||
		filepath.Base(filepath.Dir(path)) == segment
}
