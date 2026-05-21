# gin

[![GoDoc](https://godoc.org/github.com/G-Node/gin-cli?status.svg)](http://godoc.org/github.com/G-Node/gin-cli)

**Fork of [G-Node/gin-cli](https://github.com/G-Node/gin-cli) — the G-Node Infrastructure Command Line Client**

This is a fork focused on fixing the CLI experience. The original GIN server ([gin.g-node.org](https://gin.g-node.org)) is maintained by the G-Node team at LMU Munich — this fork only changes the command-line client.

## What's different

- **Upload is like rsync** — no more "Adding file changes" / re-adding files every run. Upload only transfers data.
- **Live progress display** — per-file progress bar, transfer rate, ETA, elapsed time
- **`--dry-run` flag** — see how many files would be uploaded before actually doing it
- **Resumable** — Ctrl+C at any time, re-run and it picks up where it left off
- **No terminal spam** — file counting during add phase is a single counter, not 900 lines
- **Better defaults** — upload skips the add/commit phase for already-tracked files

## Install

```bash
# Replace Homebrew's gin
brew untap g-node/pkg
# Build from source
git clone https://github.com/kylekahraman/gin.git
cd gin
go build -o gin ./main.go
cp gin /usr/local/bin/
```

Or use the prebuilt binary from [Releases](https://github.com/kylekahraman/gin/releases).

## Usage

```bash
# See what would be uploaded
gin upload --dry-run

# Upload everything (skips add/commit if nothing changed)
gin upload

# Upload with specific paths
gin upload .

# Upload with progress for specific files
gin upload data/subject_01/
```

## Why fork?

The original [G-Node/gin-cli](https://github.com/G-Node/gin-cli) has been effectively unmaintained since 2021 — 74 open issues, 0 open pull requests, last code commit February 2023. The GIN server is still actively maintained by the G-Node team at LMU, but the CLI needed UX improvements that weren't coming.

This fork keeps full compatibility with `gin.g-node.org` and adds:
- Progress bars with ETA for uploads
- No unnecessary re-adding of files on re-runs
- Rsync-style resume (Ctrl+C safe)
- Overall UX fixes from the open issues

## License

Same as the original — [MIT License](LICENSE).
