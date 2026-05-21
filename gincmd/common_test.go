package gincmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/kylekahraman/gin/git"
)

// captureStdout runs f and returns everything written to stdout
func captureStdout(f func()) string {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	f()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

func TestPrintUploadProgress_NothingToUpload(t *testing.T) {
	ch := make(chan git.RepoFileStatus)
	close(ch) // no statuses means 0 files to upload

	output := captureStdout(func() {
		printUploadProgress(ch, 0)
	})

	if !strings.Contains(output, "already on remote") {
		t.Errorf("Expected 'already on remote' message, got: %q", output)
	}
}

func TestPrintUploadProgress_EmptyChannel(t *testing.T) {
	ch := make(chan git.RepoFileStatus)
	close(ch)

	output := captureStdout(func() {
		printUploadProgress(ch, 5)
	})

	if !strings.Contains(output, "Nothing to upload") {
		t.Errorf("Expected 'Nothing to upload' message, got: %q", output)
	}
}

func TestPrintUploadProgress_AllSkipped(t *testing.T) {
	ch := make(chan git.RepoFileStatus, 10)
	go func() {
		for i := 0; i < 3; i++ {
			fname := fmt.Sprintf("sub-%03d/data.nii", i+1)
			ch <- git.RepoFileStatus{
				FileName: fname,
				Progress: "skipped",
				Note:     "already on remote",
			}
		}
		close(ch)
	}()

	output := captureStdout(func() {
		printUploadProgress(ch, 3)
	})

	// Should show checkmark for each skipped file
	if !strings.Contains(output, "✓") {
		t.Errorf("Expected checkmark for skipped files, got: %q", output)
	}
	// Should show skipped count
	if !strings.Contains(output, "3 skipped") {
		t.Errorf("Expected '3 skipped' count, got: %q", output)
	}
}

func TestPrintUploadProgress_AllComplete(t *testing.T) {
	ch := make(chan git.RepoFileStatus, 20)
	go func() {
		for i := 0; i < 5; i++ {
			fname := fmt.Sprintf("sub-%03d/T1w.nii", i+1)
			filesize := 100 * 1024 * 1024 // 100 MB per file
			// Send progress updates
			for pct := 0; pct <= 100; pct += 25 {
				bytes := int(float64(pct) / 100.0 * float64(filesize))
				ch <- git.RepoFileStatus{
					FileName:      fname,
					Progress:      fmt.Sprintf("%d%%", pct),
					Rate:          "50 MiB/s",
					ByteProgress:  bytes,
					TotalSize:     filesize,
				}
			}
			// Send completion
			ch <- git.RepoFileStatus{
				FileName:      fname,
				Progress:      "100%",
				ByteProgress:  filesize,
				TotalSize:     filesize,
			}
		}
		close(ch)
	}()

	output := captureStdout(func() {
		printUploadProgress(ch, 5)
	})

	// Should show completion count
	if !strings.Contains(output, "5/5") {
		t.Errorf("Expected '5/5' completion count, got: %q", output)
	}
	// Should have multiple sub lines
	for i := 1; i <= 5; i++ {
		fname := fmt.Sprintf("sub-%03d", i)
		if !strings.Contains(output, fname) {
			t.Errorf("Expected filename %q in output, got: %q", fname, output)
		}
	}
}

func TestPrintUploadProgress_MixedCompleteSkippedFailed(t *testing.T) {
	ch := make(chan git.RepoFileStatus, 20)
	go func() {
		// File 1: completes normally
		ch <- git.RepoFileStatus{FileName: "ok.nii", Progress: "50%", ByteProgress: 50, TotalSize: 100, Rate: "10 MiB/s"}
		ch <- git.RepoFileStatus{FileName: "ok.nii", Progress: "100%", ByteProgress: 100, TotalSize: 100}

		// File 2: skipped
		ch <- git.RepoFileStatus{FileName: "skip.nii", Progress: "skipped", Note: "already on remote"}

		// File 3: fails
		ch <- git.RepoFileStatus{FileName: "fail.nii", Err: fmt.Errorf("permission denied")}

		close(ch)
	}()

	output := captureStdout(func() {
		printUploadProgress(ch, 3)
	})

	if !strings.Contains(output, "1 skipped") {
		t.Errorf("Expected '1 skipped' count, got: %q", output)
	}
	if !strings.Contains(output, "1 failed") {
		t.Errorf("Expected '1 failed' count, got: %q", output)
	}
}

func TestPrintUploadProgress_ResumedTransfer(t *testing.T) {
	ch := make(chan git.RepoFileStatus, 10)
	go func() {
		fname := "big_file.nii"
		filesize := 1000 * 1024 * 1024 // 1 GB
		// Resumed: first progress shows bytes > 0
		ch <- git.RepoFileStatus{
			FileName:      fname,
			Progress:      "30%",
			ByteProgress:  300 * 1024 * 1024,
			TotalSize:     filesize,
			Rate:          "50 MiB/s",
			Note:          "",
		}
		ch <- git.RepoFileStatus{FileName: fname, Progress: "50%", ByteProgress: 500 * 1024 * 1024, TotalSize: filesize, Rate: "50 MiB/s"}
		ch <- git.RepoFileStatus{FileName: fname, Progress: "100%", ByteProgress: filesize, TotalSize: filesize}
		close(ch)
	}()

	output := captureStdout(func() {
		printUploadProgress(ch, 1)
	})

	if !strings.Contains(output, "1/1") {
		t.Errorf("Expected '1/1' completion, got: %q", output)
	}
	if !strings.Contains(output, "big_file.nii") {
		t.Errorf("Expected filename in output, got: %q", output)
	}
}

func TestPrintUploadProgress_SkipOnInitialZeroBytes(t *testing.T) {
	// Edge case: a progress message arrives with 0/0 bytes before the real progress
	ch := make(chan git.RepoFileStatus, 10)
	go func() {
		ch <- git.RepoFileStatus{FileName: "zero.nii", Progress: "0%", ByteProgress: 0, TotalSize: 0, Rate: ""}
		ch <- git.RepoFileStatus{FileName: "zero.nii", Progress: "50%", ByteProgress: 50, TotalSize: 100, Rate: "10 MiB/s"}
		ch <- git.RepoFileStatus{FileName: "zero.nii", Progress: "100%", ByteProgress: 100, TotalSize: 100}
		close(ch)
	}()

	output := captureStdout(func() {
		printUploadProgress(ch, 1)
	})

	if !strings.Contains(output, "1/1") {
		t.Errorf("Expected completion, got: %q", output)
	}
}
