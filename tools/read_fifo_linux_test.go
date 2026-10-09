//go:build linux

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/airlockrun/goai/tool"
	"golang.org/x/sys/unix"
)

// Each child has a parent-enforced timeout: neither a blocked FIFO open nor an
// uncancellable partial header read can leak a goroutine into the test process.
func runReadFIFOChild(t *testing.T, path, mode string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReadFIFOHelperProcess$")
	cmd.WaitDelay = time.Second
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		if name != "SOL_READ_FIFO_PATH" && name != "SOL_READ_FIFO_MODE" {
			cmd.Env = append(cmd.Env, variable)
		}
	}
	cmd.Env = append(cmd.Env, "SOL_READ_FIFO_PATH="+path, "SOL_READ_FIFO_MODE="+mode)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("FIFO operation did not terminate within its cleanup deadline: %s", output)
	}
	if err != nil {
		t.Fatalf("FIFO child failed: %v\n%s", err, output)
	}
}

func TestReadFIFOHelperProcess(t *testing.T) {
	path := os.Getenv("SOL_READ_FIFO_PATH")
	if path == "" {
		return
	}
	mode := os.Getenv("SOL_READ_FIFO_MODE")
	if mode == "open replaced path" {
		file, err := openReadFile(path)
		if file != nil {
			file.Close()
			t.Fatal("opened replacement FIFO passed descriptor validation")
		}
		if !errors.Is(err, errReadNonRegular) {
			t.Fatalf("replacement FIFO error = %v", err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	wantErr := errReadNonRegular
	if mode == "cancelled" {
		cancel()
		wantErr = context.Canceled
	}
	input, err := json.Marshal(ReadInput{FilePath: path})
	if err != nil {
		t.Fatal(err)
	}
	result, err := Read().Execute(ctx, input, tool.CallOptions{})
	if !errors.Is(err, wantErr) || len(result.Attachments) != 0 {
		t.Fatalf("FIFO result = %+v, error = %v, want %v", result, err, wantErr)
	}
}

func TestReadTool_FIFOWithoutWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image.bin")
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	runReadFIFOChild(t, path, "no writer")
}

func TestReadTool_FIFOPartialHeaderAndCancellation(t *testing.T) {
	for _, mode := range []string{"partial header", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "image.png")
			if err := unix.Mkfifo(path, 0600); err != nil {
				t.Fatal(err)
			}
			// Keep both ends open so the eight-byte PNG signature cannot produce
			// EOF. A 512-byte ReadFull on this FIFO blocks even after ctx expires.
			fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { unix.Close(fd) })
			header := []byte("\x89PNG\r\n\x1a\n")
			if n, err := unix.Write(fd, header); err != nil || n != len(header) {
				t.Fatalf("write partial header: n=%d error=%v", n, err)
			}
			runReadFIFOChild(t, path, mode)
		})
	}
}

func TestOpenReadFile_ReplacementFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replace.png")
	if err := os.WriteFile(path, readTestImage(t, "png"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("pre-open path must pass the regular-file check", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	// The opening stage must independently reject this replacement, without a
	// writer being present and without trying to read a header from the FIFO.
	runReadFIFOChild(t, path, "open replaced path")
}

func TestReadTool_RegularSymlinkTargets(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"text", []byte("ordinary text\n")},
		{"PNG", readTestImage(t, "png")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "target")
			if err := os.WriteFile(target, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "link")
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			input, _ := json.Marshal(ReadInput{FilePath: path})
			result, err := Read().Execute(t.Context(), input, tool.CallOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "PNG" {
				if len(result.Attachments) != 1 || result.Attachments[0].MimeType != "image/png" {
					t.Fatal("regular image symlink did not return an image attachment")
				}
			} else if !strings.Contains(result.Output, "ordinary text") || len(result.Attachments) != 0 {
				t.Fatal("regular text symlink changed read behavior")
			}
		})
	}
}
