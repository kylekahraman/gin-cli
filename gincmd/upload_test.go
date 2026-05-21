package gincmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupTestAnnexRepo creates a temporary git-annex repo with a fake remote
// and returns the directory path. Call cleanup on the returned func.
func setupTestAnnexRepo(t *testing.T) (dir string, cleanup func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "gin-rich-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	run := func(name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("cmd %s %v failed: %v\n%s", name, args, err, string(out))
		}
		return string(out)
	}

	run("git", "init")
	run("git", "annex", "init", "testbox")

	// Set git user for commits
	run("git", "config", "user.email", "test@test.com")
	run("git", "config", "user.name", "Test User")

	// Create a fake remote as a local directory
	remoteDir := dir + "-remote"
	if err := os.MkdirAll(remoteDir, 0755); err != nil {
		t.Fatalf("failed to create remote dir: %v", err)
	}
	run("git", "annex", "initremote", "testremote",
		"type=directory", "directory="+remoteDir, "encryption=none")

	// Push initial git metadata to remote
	run("git", "annex", "sync", "--no-pull", "--no-commit", "testremote")

	cleanup = func() {
		// git-annex makes files immutable in .git/annex, force delete
		run("chmod", "-R", "u+w", dir)
		os.RemoveAll(dir)
		os.RemoveAll(remoteDir)
	}
	return dir, cleanup
}

func TestCommitPhase_SkipsWhenNoChanges(t *testing.T) {
	dir, cleanup := setupTestAnnexRepo(t)
	defer cleanup()

	// Create and commit an initial file
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello"), 0644)
	runCmd(t, dir, "git", "annex", "add", "test.txt")
	runCmd(t, dir, "git", "commit", "-m", "initial")

	// Now run git annex status on the directory — should be clean
	cmd := exec.Command("git", "annex", "status", "--json", ".")
	cmd.Dir = dir
	out, _ := cmd.Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("expected clean status but got: %s", string(out))
	}

	// Verify: annex add --json . outputs nothing
	cmd2 := exec.Command("git", "annex", "add", "--json", ".")
	cmd2.Dir = dir
	out2, _ := cmd2.Output()
	if strings.TrimSpace(string(out2)) != "" {
		t.Fatalf("expected no output from add --json but got: %s", string(out2))
	}
}

func TestCommitPhase_DetectsUntrackedFiles(t *testing.T) {
	dir, cleanup := setupTestAnnexRepo(t)
	defer cleanup()

	// Create a file but don't add it
	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new content"), 0644)

	// git annex status should show it as untracked (?)
	cmd := exec.Command("git", "annex", "status", "--json", ".")
	cmd.Dir = dir
	out, _ := cmd.Output()

	if !strings.Contains(string(out), `"status":"?"`) {
		t.Fatalf("expected untracked status for new.txt, got: %s", string(out))
	}

	// Now add just this specific file
	cmd3 := exec.Command("git", "annex", "add", "--json", "new.txt")
	cmd3.Dir = dir
	out3, _ := cmd3.CombinedOutput()

	if !strings.Contains(string(out3), `"file":"new.txt"`) {
		t.Fatalf("expected add to process new.txt, got: %s", string(out3))
	}
}

func TestCommitPhase_OnlyUntrackedFilesAreAdded(t *testing.T) {
	dir, cleanup := setupTestAnnexRepo(t)
	defer cleanup()

	// Create and commit one file
	os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("tracked"), 0644)
	os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("untracked"), 0644)
	runCmd(t, dir, "git", "annex", "add", "tracked.txt")
	runCmd(t, dir, "git", "commit", "-m", "initial")

	// Now: tracked.txt is committed, untracked.txt is still ?
	// Run git annex add --json . and see how many files it processes
	cmd := exec.Command("git", "annex", "add", "--json", ".")
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput()

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	// Should only process the untracked file, not the already-tracked one
	if len(lines) != 1 {
		t.Fatalf("expected 1 file processed (untracked.txt), got %d: %s", len(lines), string(out))
	}
	if !strings.Contains(string(out), "untracked.txt") {
		t.Fatalf("expected untracked.txt, got: %s", string(out))
	}
}

func TestCommitPhase_ResumeInterruptedUpload(t *testing.T) {
	dir, cleanup := setupTestAnnexRepo(t)
	defer cleanup()

	// Simulate: first upload added files and synced, but didn't finish
	os.WriteFile(filepath.Join(dir, "data1.txt"), []byte("data1"), 0644)
	os.WriteFile(filepath.Join(dir, "data2.txt"), []byte("data2"), 0644)
	runCmd(t, dir, "git", "annex", "add", "data1.txt", "data2.txt")
	runCmd(t, dir, "git", "commit", "-m", "first batch")

	// Simulate interrupt: git annex sync was partially run
	runCmd(t, dir, "git", "annex", "sync", "--no-pull", "--no-commit", "testremote")

	// Now check: git annex status should be clean (no re-add needed)
	cmd := exec.Command("git", "annex", "status", "--json", ".")
	cmd.Dir = dir
	out, _ := cmd.Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("expected clean status after sync, got: %s", string(out))
	}

	// git annex add --json . should output nothing
	cmd2 := exec.Command("git", "annex", "add", "--json", ".")
	cmd2.Dir = dir
	out2, _ := cmd2.CombinedOutput()
	if strings.TrimSpace(string(out2)) != "" {
		t.Fatalf("expected no files to add after sync, got: %s", string(out2))
	}
}

// runCmd runs a command and fails the test on error
func runCmd(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cmd %s %v failed: %v\n%s", name, args, err, string(out))
	}
}
