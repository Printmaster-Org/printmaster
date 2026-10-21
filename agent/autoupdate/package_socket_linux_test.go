package autoupdate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"printmaster/common/updatetrust"
)

func TestPrivilegedRepositoryPackageSocketExchange(t *testing.T) {
	for _, mode := range []string{"valid", "rejected", "cancelled", "stalled", "wrong target", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			// Unix socket path limits are much shorter than Windows/WSL test paths.
			dir, err := os.MkdirTemp("/tmp", "pm-socket-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			path := filepath.Join(dir, "intake")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err := listener.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
				t.Fatal(err)
			}
			body := bytes.Repeat([]byte("package"), 512*1024)
			_, target := packageRequestTestFrame(t, body)
			source, err := os.CreateTemp(t.TempDir(), "package")
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			if _, err := source.Write(body); err != nil {
				t.Fatal(err)
			}
			budget := 5 * time.Second
			if mode == "stalled" {
				budget = 150 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			served := make(chan error, 1)
			clientFinished := make(chan struct{})
			go func() {
				connection, err := listener.AcceptUnix()
				if err != nil {
					served <- err
					return
				}
				defer connection.Close()
				if os.Geteuid() != 0 {
					// Client must reject the service-user peer before sending.
					buffer := make([]byte, 1)
					n, err := connection.Read(buffer)
					if n != 0 {
						served <- errors.New("nonroot peer received package data")
					} else if err == nil {
						served <- errors.New("connection did not close")
					} else {
						served <- nil
					}
					return
				}
				if mode == "cancelled" {
					cancel()
					<-ctx.Done()
					served <- nil
					return
				}
				if mode == "stalled" {
					<-clientFinished
					served <- nil
					return
				}
				input, err := connection.File()
				if err != nil {
					served <- err
					return
				}
				defer input.Close()
				if mode == "rejected" {
					served <- writeRepositoryPackageOutcome(ctx, input, RepositoryPackageOutcome{Schema: 1, Status: "rejected"})
					return
				}
				err = receiveRepositoryPackage(ctx, input, dir, func(file *os.File, got updatetrust.Target) error {
					if got != target {
						return errors.New("socket target changed")
					}
					actual, err := packageTargetFromFile(ctx, file, got)
					if err != nil || actual != target {
						return errors.Join(errors.New("socket bytes changed"), err)
					}
					return nil
				})
				if err == nil {
					if mode == "malformed" {
						_, err = input.Write([]byte{0, 0, 0, 1, '{'})
					} else {
						replyTarget := target
						if mode == "wrong target" {
							replyTarget.Version = "9.0.0"
						}
						err = writeRepositoryPackageOutcome(ctx, input, RepositoryPackageOutcome{
							Schema: 1, Status: "awaiting-startup", RunID: "0123456789abcdef0123456789abcdef", Target: &replyTarget,
						})
					}
				}
				served <- err
			}()
			start := time.Now()
			outcome, err := exchangeRepositoryPackageSocket(ctx, path, source, target)
			close(clientFinished)
			wantSuccess := os.Geteuid() == 0 && mode == "valid"
			if (err == nil) != wantSuccess || wantSuccess && outcome.Status != "awaiting-startup" {
				t.Fatalf("socket exchange outcome=%+v error=%v", outcome, err)
			}
			if serverErr := <-served; serverErr != nil {
				t.Fatal(serverErr)
			}
			if mode == "stalled" && time.Since(start) > time.Second {
				t.Fatal("socket cancellation exceeded one second")
			}
			if offset, err := source.Seek(0, io.SeekCurrent); err != nil || offset != int64(len(body)) {
				t.Fatalf("source offset changed: %d %v", offset, err)
			}
		})
	}
}

func TestPrivilegedRepositoryPackageSocketProtection(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("socket ownership checks require root")
	}
	dir, err := os.MkdirTemp("/tmp", "pm-socket-owner-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "intake")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for _, mode := range []os.FileMode{0o660, 0o600, 0o666} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if err := validateRepositoryPackageSocket(path); (err == nil) != (mode == 0o660) {
			t.Fatalf("incorrect socket mode acceptance: %o %v", mode, err)
		}
	}
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := validateRepositoryPackageSocket(link); err == nil {
		t.Fatal("symlink accepted as protected socket")
	}
	if err := os.Chown(path, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	if err := validateRepositoryPackageSocket(path); err == nil {
		t.Fatal("nonroot socket owner accepted")
	}
	if err := os.Chown(path, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := validateRepositoryPackageSocket(path); err == nil {
		t.Fatal("writable socket parent accepted")
	}
}

func TestPrivilegedRepositoryPackageSocketAuthenticated(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root trust/policy/journal socket rehearsal requires root")
	}
	for _, mode := range []string{"valid", "install failure", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "pm-root-socket-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "intake"), Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err := listener.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			body := bytes.Repeat([]byte("package"), 64*1024)
			_, target := packageRequestTestFrame(t, body)
			manifest := &UpdateManifest{Component: target.Component, Channel: target.Channel, Version: target.Version,
				Platform: target.Platform, Arch: target.Arch, Format: target.Format, SHA256: target.SHA256, SizeBytes: target.SizeBytes}
			_, metadataDir := runtimeTrustRepository(t, manifest, body, time.Now())
			server := httptest.NewServer(http.FileServer(http.Dir(metadataDir)))
			defer server.Close()
			policy := `{"schema":1,"enabled":true}`
			if mode == "disabled" {
				policy = `{"schema":1,"enabled":false}`
			}
			policyPath := filepath.Join(dir, "policy.json")
			if err := os.WriteFile(policyPath, []byte(policy), 0o600); err != nil {
				t.Fatal(err)
			}
			source, err := os.CreateTemp(dir, "input")
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			if _, err := source.Write(body); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			served := make(chan error, 1)
			var diagnostics bytes.Buffer
			state := filepath.Join(dir, "state")
			go func() {
				connection, err := listener.AcceptUnix()
				if err != nil {
					served <- err
					return
				}
				defer connection.Close()
				stream, err := connection.File()
				if err != nil {
					served <- err
					return
				}
				defer stream.Close()
				// Match systemd StandardInput=socket/StandardOutput=socket with
				// real inherited descriptors; only native commands are mocked.
				child := exec.CommandContext(ctx, executable, "-test.run=^TestPrivilegedRepositoryPackageProcessChild$")
				child.Stdin, child.Stdout, child.Stderr = stream, stream, &diagnostics
				child.Env = append(os.Environ(), "PRINTMASTER_PACKAGE_TEST_CHILD="+mode,
					"PRINTMASTER_PACKAGE_TEST_ROOT="+filepath.Join(metadataDir, "1.root.json"),
					"PRINTMASTER_PACKAGE_TEST_URL="+server.URL, "PRINTMASTER_PACKAGE_TEST_STATE="+state,
					"PRINTMASTER_PACKAGE_TEST_POLICY="+policyPath)
				served <- child.Run()
			}()
			outcome, exchangeErr := exchangeRepositoryPackageSocket(ctx, listener.Addr().String(), source, target)
			childErr := <-served
			if mode == "valid" {
				if exchangeErr != nil || childErr != nil || outcome.Status != "awaiting-startup" {
					t.Fatalf("socket helper failed: %+v %v %v %s", outcome, exchangeErr, childErr, diagnostics.String())
				}
			} else if exchangeErr == nil || childErr == nil {
				t.Fatalf("failed socket transaction claimed completion: %+v %v %v", outcome, exchangeErr, childErr)
			}
			if mode == "disabled" {
				if _, err := os.Lstat(filepath.Join(state, packageTransactionFile)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("disabled policy recorded install intent: %v", err)
				}
				return
			}
			journal, err := InspectPrivilegedRepositoryPackage(context.Background(), state)
			if err != nil {
				t.Fatal(err)
			}
			phase := "awaiting-startup"
			if mode == "install failure" {
				phase = "recovery-required"
			}
			if journal.Target != target || journal.Phase != phase || mode == "valid" && journal.RunID != outcome.RunID {
				t.Fatalf("socket reply disagrees with protected journal: %+v %+v", journal, outcome)
			}
		})
	}
}

func TestRepositoryPackageTransportRejectsDatagramAndFiles(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "not-a-socket")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := pollablePackagePipe(file); err == nil {
		t.Fatal("regular file accepted as request transport")
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	first, second := os.NewFile(uintptr(fds[0]), "datagram"), os.NewFile(uintptr(fds[1]), "datagram-peer")
	defer first.Close()
	defer second.Close()
	if _, err := pollablePackagePipe(first); err == nil {
		t.Fatal("datagram accepted as stream transport")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	tcp, err := listener.(*net.TCPListener).File()
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	if _, err := pollablePackagePipe(tcp); err == nil {
		t.Fatal("TCP socket accepted as Unix transport")
	}
}
