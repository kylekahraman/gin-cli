package gincmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"time"

	ginclient "github.com/kylekahraman/gin/ginclient"
	"github.com/kylekahraman/gin/ginclient/log"
	"github.com/kylekahraman/gin/git"
	"github.com/bbrks/wrap"
	"github.com/docker/docker/pkg/term"
	"github.com/dustin/go-humanize"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

const (
	unknownhostname = "(unknown)"
	jsonHelpMsg     = "Print output in JSON format."
	verboseHelpMsg  = "Print underlying git and git-annex calls and their unmodified output."
)

var (
	green  = color.New(color.FgGreen).SprintFunc()
	red    = color.New(color.FgRed).SprintFunc()
	yellow = color.New(color.FgYellow).SprintFunc()
	cyan   = color.New(color.FgCyan).SprintFunc()

	reqgitannex = []string{
		"add-remote",
		"commit",
		"create",
		"download",
		"get",
		"get-content",
		"init",
		"lock",
		"ls",
		"remotes",
		"remove-content",
		"remove-remote",
		"unlock",
		"upload",
		"use-remote",
		"version",
	}
)

type printstyle uint8

const (
	psDefault printstyle = iota
	psProgress
	psJSON
	psVerbose
)

// Die prints an error message to stderr and exits the program with status 1.
func Die(msg interface{}) {
	msgstring := fmt.Sprintf("%s", msg)
	if len(msgstring) > 0 {
		log.Write("Exiting with ERROR message: %s", msgstring)
		fmt.Fprintf(color.Error, "%s %s\n", red("[error]"), msgstring)
	} else {
		log.Write("Exiting with ERROR (no message)")
	}
	log.Close()
	os.Exit(1)
}

// Warn prints a warning message to stderr, logs it, and returns without interruption.
func Warn(msg string) {
	log.Write("Showing warning: %q", msg)
	fmt.Fprintf(color.Error, "%s %s\n", yellow("[warning]"), msg)
}

// Exit prints a message to stdout and exits the program with status 0.
func Exit(msg string) {
	if len(msg) > 0 {
		log.Write("Exiting with message: %s", msg)
		fmt.Println(msg)
	} else {
		log.Write("Exiting")
	}
	log.Close()
	os.Exit(0)
}

// CheckError exits the program if an error is passed to the function.
// The error message is checked for known error messages and an informative message is printed.
// Otherwise, the error message is printed to stderr.
func CheckError(err error) {
	if err != nil {
		log.Write(err.Error())
		if strings.Contains(err.Error(), "Error loading user token") {
			Die("This operation requires login.")
		}
		Die(err)
	}
}

// CheckErrorMsg exits the program if an error is passed to the function.
// Before exiting, the given msg string is printed to stderr.
func CheckErrorMsg(err error, msg string) {
	if err != nil {
		log.Write("The following error occurred:\n%sExiting with message: %s", err, msg)
		Die(msg)
	}
}

// requirelogin prompts for login if the user is not already logged in.
// It only checks if a local token exists and does not confirm its validity with the server.
// The function should be called at the start of any command that requires being logged in to run.
func requirelogin(cmd *cobra.Command, gincl *ginclient.Client, prompt bool) {
	gincl.LoadToken()
}

func annexVersionNotice() {
	msg := `The current repository is using an old layout for annexed data.  It is recommended that you upgrade to the newest version.  You may still use it as is for now, but in the future the upgrade will happen automatically.  This message will continue to appear for affected git-annex operations until you upgrade.

Run 'gin annex upgrade' followed by 'gin init' to upgrade this repository now.  The operation should only take a few seconds.

Visit the following page for a description of how this change may affect your workflow:`
	w := termwidth()
	if w > 80 {
		w = 80
	}
	msg = wrap.Wrap(msg, w)
	// append URL after wrapping to avoid breaking the URL with spaces
	msg += "https://gin.g-node.org/G-Node/gin-cli-releases/src/master/v1.7#changes"
	fmt.Println(msg)
}

func usageDie(cmd *cobra.Command) {
	cmd.Help()
	// exit without message
	Die("")
}

func printJSON(statuschan <-chan git.RepoFileStatus) (filesuccess map[string]bool) {
	filesuccess = make(map[string]bool)
	for stat := range statuschan {
		j, _ := json.Marshal(stat)
		fmt.Println(string(j))
		filesuccess[stat.FileName] = true
		if stat.Err != nil {
			filesuccess[stat.FileName] = false
		}
	}
	return
}

func printProgressWithBar(statuschan <-chan git.RepoFileStatus, nitems int) (filesuccess map[string]bool) {
	if nitems <= 0 {
		// If nitems is invalid, just print the classic progress output
		return printProgressOutput(statuschan)
	}
	ndigits := len(fmt.Sprintf("%d", nitems))
	dfmt := fmt.Sprintf("%%%dd/%%%dd", ndigits, ndigits) // dynamic formatting string adapts to number of digits in item count
	filesuccess = make(map[string]bool)

	// closure binds ndigits and nitems but keeps bar width dynamic so it can
	// adapt to terminal resizing -- introduces a few operations per refresh,
	// but makes the bar printing much nicer
	printbar := func(completed int) int {
		linewidth := termwidth()
		if linewidth > 80 {
			linewidth = 80
		} else if linewidth < 30 {
			// Skip bar printing for very small terminals
			fmt.Println()
			return 0
		}
		fullbarwidth := linewidth - (5 + ndigits*2)
		if fullbarwidth < 0 {
			// Again, skip bar printing if ndigits is so large that a bar would
			// have negative width.  Since we skip printing the bar if the
			// termwidth is < 30, this can only happen when processing more
			// than 1e13 files, but if we ever change the min width to
			// something smaller than 30 (or make it dynamic somehow), this
			// guard will be useful.
			fmt.Println()
			return 0
		}
		barratio := float64(fullbarwidth) / float64(nitems)

		complsigns := int(math.Floor(float64(completed) * barratio))
		blocks := strings.Repeat("=", complsigns)
		blanks := strings.Repeat(" ", fullbarwidth-complsigns)
		dprg := fmt.Sprintf(dfmt, completed, nitems)
		fmt.Printf("\n [%s%s] %s\r", blocks, blanks, dprg)
		return linewidth
	}

	outline := new(bytes.Buffer)
	outappend := func(part string) {
		if len(part) > 0 {
			outline.WriteString(part)
			outline.WriteString(" ")
		}
	}

	printed := false
	prevlinewidth := 0
	ncompleted := 0
	for stat := range statuschan {
		ncompleted++
		if ncompleted > nitems {
			// BUG: Not sure when this occurs, but it's been happening in the
			// CI environment for some remove-content calls and I haven't been
			// able to reproduce - AK, 2019-07-07
			nitems = ncompleted
		}
		outline.Reset()
		outline.WriteString(" ")
		outappend(stat.State)
		if stat.FileName != "" {
			outappend(fmt.Sprintf("%q", stat.FileName))
		}
		if stat.Err == nil {
			if stat.Progress == "100%" {
				outappend(green("OK"))
				filesuccess[stat.FileName] = true
			}
		} else {
			outappend(stat.Err.Error())
			filesuccess[stat.FileName] = false
		}
		newprint := outline.String()
		fmt.Printf("\r%s\r", strings.Repeat(" ", prevlinewidth)) // clear the line
		fmt.Fprint(color.Output, newprint)
		prevlinewidth = printbar(ncompleted)
		printed = true
	}
	if !printed {
		fmt.Println("   Nothing to do")
	}
	if outline.Len() > 0 {
		fmt.Println()
	}
	return
}

func printProgressOutput(statuschan <-chan git.RepoFileStatus) (filesuccess map[string]bool) {
	filesuccess = make(map[string]bool)
	var fname, state string
	var lastprint string
	outline := new(bytes.Buffer)
	outappend := func(part string) {
		if len(part) > 0 {
			outline.WriteString(part)
			outline.WriteString(" ")
		}
	}

	printed := false
	for stat := range statuschan {
		outline.Reset()
		outline.WriteString(" ")
		if stat.FileName != fname || stat.State != state {
			// New line if new file or new state
			if len(lastprint) > 0 {
				fmt.Println()
			}
			lastprint = ""
			fname = stat.FileName
			state = stat.State
		}
		outappend(stat.State)
		if stat.FileName != "" {
			outappend(fmt.Sprintf("%q", stat.FileName))
		}
		if stat.Err == nil {
			if stat.Progress == "100%" {
				outappend(green("OK"))
				filesuccess[stat.FileName] = true
			} else {
				outappend(stat.Progress)
				outappend(stat.Rate)
			}
		} else {
			log.WriteError(stat.Err)
			outappend(stat.Err.Error())
			filesuccess[stat.FileName] = false
		}
		newprint := outline.String()
		if newprint != lastprint {
			fmt.Printf("\r%s\r", strings.Repeat(" ", len(lastprint))) // clear the line
			fmt.Fprint(color.Output, newprint)
			fmt.Print("\r")
			lastprint = newprint
			printed = true
		}
	}
	if !printed {
		fmt.Println("   Nothing to do")
	}
	if len(lastprint) > 0 {
		fmt.Println()
	}
	return
}

func verboseOutput(statuschan <-chan git.RepoFileStatus) (filesuccess map[string]bool) {
	filesuccess = make(map[string]bool)
	var tmprawin string
	for stat := range statuschan {
		//Raw Input
		if stat.RawInput != tmprawin {
			fmt.Printf("Running Command: %v\n", stat.RawInput)
			tmprawin = stat.RawInput
		}
		//Raw Output
		fmt.Print(stat.RawOutput)
	}
	fmt.Println()
	return
}

// determinePrintStyle determines the print style to use based on the flags supplied by the user and the subcommand that is being called.
// If incompatible flags are received (--json and --verbose), it immediately exits using Die().
func determinePrintStyle(cmd *cobra.Command) printstyle {
	verboseOn, _ := cmd.Flags().GetBool("verbose")
	jsonOn, _ := cmd.Flags().GetBool("json")

	isProgressCmd := func() bool {
		progressCmds := []string{"lock", "unlock", "remove-content"}
		for _, cname := range progressCmds {
			if cname == cmd.Name() {
				return true
			}
		}
		return false
	}

	switch {
	case verboseOn && jsonOn:
		Die("--verbose and --json cannot be used together")
	case verboseOn:
		git.RawMode = true
		return psVerbose
	case jsonOn:
		return psJSON
	case isProgressCmd():
		return psProgress
	default:
		return psDefault
	}
	return psDefault
}

func formatOutput(statuschan <-chan git.RepoFileStatus, pstyle printstyle, nitems int) {
	// TODO: instead of a true/false success, add an error for every file and then group the errors by type and print a report
	var filesuccess map[string]bool
	switch pstyle {
	case psJSON:
		filesuccess = printJSON(statuschan)
	case psVerbose:
		filesuccess = verboseOutput(statuschan)
	case psProgress:
		filesuccess = printProgressWithBar(statuschan, nitems)
	case psDefault:
		filesuccess = printProgressOutput(statuschan)
	}

	// count unique file errors
	nerrors := 0
	for _, stat := range filesuccess {
		if !stat {
			nerrors++
		}
	}
	if nerrors > 0 {
		// Exit with error message and failed exit status
		var plural string
		if nerrors > 1 {
			plural = "s"
		}
		Die(fmt.Sprintf("%d operation%s failed", nerrors, plural))
	}
}

// Progress display helpers reused across printUploadProgress.
//
// fmtbytes formats a byte count as a human-readable string (e.g. "45.2 MB").
func fmtbytes(b int64) string {
	if b < 0 {
		return "0 B"
	}
	return humanize.IBytes(uint64(b))
}

// fmtDuration formats a duration as a compact string (e.g. "[1m23s]").
func fmtDuration(d time.Duration) string {
	secs := int(d.Seconds())
	if secs <= 0 {
		return ""
	}
	if secs < 60 {
		return fmt.Sprintf("[%ds]", secs)
	}
	mins := secs / 60
	secs = secs % 60
	if mins < 60 {
		return fmt.Sprintf("[%dm%02ds]", mins, secs)
	}
	hrs := mins / 60
	mins = mins % 60
	return fmt.Sprintf("[%dh%02dm]", hrs, mins)
}

// etaFromRate returns an ETA string given remaining bytes and a rate in bytes/sec.
func etaFromRate(remBytes int64, rate float64) string {
	if rate <= 0 || remBytes <= 0 {
		return ""
	}
	secs := float64(remBytes) / rate
	if secs < 1 {
		return ""
	}
	if secs < 60 {
		return fmt.Sprintf("%.0fs", secs)
	}
	mins := int(secs) / 60
	s := int(secs) % 60
	if mins < 60 {
		return fmt.Sprintf("%dm%02ds", mins, s)
	}
	hrs := mins / 60
	mins = mins % 60
	return fmt.Sprintf("%dh%02dm", hrs, mins)
}

// truncName shortens a filename for display, keeping the extension visible.
func truncName(name string, maxLen int) string {
	if idx := strings.Index(name, " (version: "); idx > 0 {
		name = name[:idx]
	}
	if len(name) <= maxLen {
		return name
	}
	// Show first 2 chars + "…" + last (maxLen-3) chars
	return name[:2] + "…" + name[len(name)-(maxLen-3):]
}

// fileState tracks the transfer progress of a single file.
type fileState struct {
	done      bool
	skipped   bool
	failed    bool
	lastPct   string
	lastBytes int
	totalSize int
	rate      string
	note      string
}

// printUploadProgress displays an rsync-style live progress view for uploads.
func printUploadProgress(statuschan <-chan git.RepoFileStatus, totalFiles int) (filesuccess map[string]bool) {
	filesuccess = make(map[string]bool)
	if totalFiles <= 0 {
		fmt.Println("   All files already on remote — nothing to upload")
		return
	}

	// Bar-drawing helper: returns a filled-bar string of the given width.
	makeBar := func(filled, width int) string {
		if filled < 0 {
			filled = 0
		}
		if filled > width {
			filled = width
		}
		return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	}

	seen := make(map[string]*fileState)
	var (
		completed    int
		skipped      int
		failed       int
		startTime    time.Time
		overallBytes int64
		firstData    bool
	)

	// buildLine assembles a single-line display for the current transfer state.
	width := termwidth()
	if width < 50 {
		width = 50
	}
	buildLine := func(fname string, fs *fileState, elapsed time.Duration, compl int) string {
		// --- File part ---
		filePart := ""
		if fname != "" && fs != nil {
			showname := truncName(fname, 25)

			switch {
			case fs.done:
				filePart = fmt.Sprintf("%s  %s  %s", green(showname), green("✔"), fmtbytes(int64(fs.totalSize)))
			case fs.skipped:
				filePart = fmt.Sprintf("%s  %s", cyan(showname), green("✓"))
			case fs.failed:
				filePart = fmt.Sprintf("%s  %s", red(showname), red("✖"))
			case fs.lastPct != "" && fs.totalSize > 0 && fs.lastBytes > 0:
				pct := float64(fs.lastBytes) / float64(fs.totalSize) * 100
				barW := 10
				filled := int(pct / 100 * float64(barW))
				bar := makeBar(filled, barW)

				eta := ""
				rate := parseRate(fs.rate)
				if rate > 0 {
					remBytes := int64(fs.totalSize) - int64(fs.lastBytes)
					if remBytes > 0 {
						eta = " " + etaFromRate(remBytes, rate)
					}
				}
				filePart = fmt.Sprintf("%s %s %s/%s %s%s",
					cyan(showname), bar,
					fmtbytes(int64(fs.lastBytes)), fmtbytes(int64(fs.totalSize)),
					fs.rate, eta)
			default:
				// Starting or minimal info
				filePart = cyan(showname)
			}
		}

		// --- Overall part ---
		overallPart := ""
		if totalFiles > 0 && compl > 0 {
			overallPct := float64(compl) / float64(totalFiles) * 100
			countStr := fmt.Sprintf("%d/%d", compl, totalFiles)

			// Compute overall rate
			overallRate := float64(0)
			if elapsed.Seconds() > 0 && overallBytes > 0 {
				overallRate = float64(overallBytes) / elapsed.Seconds()
			}

			// Build suffix elements
			pctStr := fmt.Sprintf("%d%%", int(overallPct))
			speedStr := ""
			if overallRate > 0 {
				speedStr = fmt.Sprintf("%s/s", humanize.IBytes(uint64(overallRate)))
			}
			etaStr := ""
			if overallRate > 0 && completed > 0 {
				remaining := totalFiles - compl
				if remaining > 0 {
					avgBytes := overallBytes / int64(completed)
					remBytes := avgBytes * int64(remaining)
					etaStr = etaFromRate(remBytes, overallRate)
					if etaStr != "" {
						etaStr = "ETA " + etaStr
					}
				}
			}
			elapsedStr := fmtDuration(elapsed)

			// Dynamic bar width: compute what's left after fixed elements
			// Fixed: " ██████░░ " (bar + 2 spaces) + pct + " " + count + speed + eta + elapsed
			suffixLen := 2 // spaces around bar
			for _, s := range []string{pctStr, countStr, speedStr, etaStr, elapsedStr} {
				if s != "" {
					suffixLen += len(s) + 1 // +1 for separator space
				}
			}

			// Reserve space for file part + " ||" separator
			filePartWidth := 0
			if filePart != "" {
				filePartWidth = lineLen(filePart)
			}
			sepLen := 0
			if filePart != "" && overallPart != "" {
				sepLen = 4 // " || "
			}

			barW := width - filePartWidth - sepLen - suffixLen - 1
			if barW < 4 {
				barW = 4
			}
			if barW > 40 {
				barW = 40
			}

			fill := int(overallPct / 100 * float64(barW))
			bar := makeBar(fill, barW)

			// Assemble overall part
			var parts []string
			parts = append(parts, bar)
			parts = append(parts, pctStr)
			parts = append(parts, countStr)
			if speedStr != "" {
				parts = append(parts, speedStr)
			}
			if etaStr != "" {
				parts = append(parts, etaStr)
			}
			if elapsedStr != "" {
				parts = append(parts, elapsedStr)
			}
			overallPart = " " + strings.Join(parts, " ")
		}

		// Add skipped/failed summary
		if skipped > 0 {
			overallPart += yellow(fmt.Sprintf(" %d skipped", skipped))
		}
		if failed > 0 {
			overallPart += red(fmt.Sprintf(" %d failed", failed))
		}

		// Combine
		line := ""
		if filePart != "" && overallPart != "" {
			line = filePart + " ||" + overallPart
		} else if filePart != "" {
			line = filePart
		} else {
			line = overallPart
		}
		return line
	}

	// drawLine clears the previous line and draws a new one.
	var lastLineLen int
	drawLine := func(fname string, fs *fileState, elapsed time.Duration, compl int) {
		line := buildLine(fname, fs, elapsed, compl)
		fmt.Printf("\r%s\r%s", strings.Repeat(" ", lastLineLen), line)
		lastLineLen = lineLen(line)
	}

	// Main event loop
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var (
		lastFname string
		lastFs    *fileState
	)
	for {
		select {
		case stat, ok := <-statuschan:
			if !ok {
				// Channel closed — final display
				compl := completed + skipped + failed
				lastLine := buildLine(lastFname, lastFs, time.Since(startTime), compl)
				fmt.Printf("\r%s\r%s\n", strings.Repeat(" ", lastLineLen), lastLine)
				if completed == 0 && skipped == 0 && failed == 0 {
					fmt.Print("   Nothing to upload")
				}
				return
			}

			if !firstData {
				startTime = time.Now()
				firstData = true
			}

			fname := stat.FileName
			lastFname = fname

			fs, ok := seen[fname]
			if !ok {
				fs = &fileState{}
				seen[fname] = fs
			}
			lastFs = fs

			switch {
			case stat.Progress == "100%":
				if !fs.done && !fs.skipped {
					fs.done = true
					fs.lastPct = "100%"
					fs.totalSize = stat.TotalSize
					completed++
					if stat.TotalSize > 0 {
						overallBytes += int64(stat.TotalSize)
					}
					filesuccess[fname] = true
				}
			case stat.Progress == "skipped":
				if !fs.done && !fs.skipped {
					fs.skipped = true
					fs.note = stat.Note
					skipped++
					filesuccess[fname] = true
				}
			case stat.Err != nil:
				if !fs.failed {
					fs.failed = true
					fs.note = stat.Err.Error()
					failed++
					filesuccess[fname] = false
				}
			default:
				fs.lastPct = stat.Progress
				fs.lastBytes = stat.ByteProgress
				fs.totalSize = stat.TotalSize
				fs.rate = stat.Rate
				fs.note = stat.Note
			}

			compl := completed + skipped + failed
			drawLine(fname, fs, time.Since(startTime), compl)

		case <-ticker.C:
			if !firstData {
				if startTime.IsZero() {
					startTime = time.Now()
				}
				// Show waiting spinner
				secs := int(time.Since(startTime).Seconds())
				dots := strings.Repeat(".", (secs%3)+1)
				msg := yellow(fmt.Sprintf("   Preparing...%s  (%ds)", dots, secs))
				fmt.Printf("\r%s\r%s", strings.Repeat(" ", lastLineLen), msg)
				lastLineLen = lineLen(msg)
			} else {
				// Periodic refresh — update elapsed time
				compl := completed + skipped + failed
				if compl < totalFiles {
					drawLine(lastFname, lastFs, time.Since(startTime), compl)
				}
			}
		}
	}
}

// lineLen returns the visible length of a string, excluding ANSI escape codes.
func lineLen(s string) int {
	// Strip ANSI escape sequences for length calculation
	inEscape := false
	length := 0
	for _, ch := range s {
		if ch == '\033' {
			inEscape = true
			continue
		}
		if inEscape {
			if ch == 'm' {
				inEscape = false
			}
			continue
		}
		length++
	}
	return length
}

// parseRate parses a human-readable rate like "12.3 MB/s" or "1.2 GB/s" back to bytes/sec
func parseRate(rate string) float64 {
	if rate == "" {
		return 0
	}
	// rates look like: "12.3 MB/s" or "1.2 GiB/s"
	parts := strings.SplitN(rate, " ", 2)
	if len(parts) != 2 {
		return 0
	}
	val := 0.0
	fmt.Sscanf(parts[0], "%f", &val)

	unit := parts[1]
	if strings.HasPrefix(unit, "B") || strings.HasPrefix(unit, "byte") {
		return val
	} else if strings.HasPrefix(unit, "KiB") || strings.HasPrefix(unit, "KB") || strings.HasPrefix(unit, "kB") {
		return val * 1024
	} else if strings.HasPrefix(unit, "MiB") || strings.HasPrefix(unit, "MB") || strings.HasPrefix(unit, "mB") {
		return val * 1024 * 1024
	} else if strings.HasPrefix(unit, "GiB") || strings.HasPrefix(unit, "GB") || strings.HasPrefix(unit, "gB") {
		return val * 1024 * 1024 * 1024
	} else if strings.HasPrefix(unit, "TiB") || strings.HasPrefix(unit, "TB") {
		return val * 1024 * 1024 * 1024 * 1024
	}
	return val
}

var wouter = wrap.NewWrapper()
var winner = wrap.NewWrapper()

func termwidth() int {
	width := 80
	if ws, err := term.GetWinsize(0); err == nil {
		width = int(ws.Width)
	}
	return width - 1
}

func formatdesc(desc string, args map[string]string) (fdescription string) {
	width := termwidth()
	wouter.OutputLinePrefix = "  "
	winner.OutputLinePrefix = "    "

	if len(desc) > 0 {
		fdescription = fmt.Sprintf("Description:\n\n%s", wouter.Wrap(desc, width))
	}

	if args != nil {
		argsdesc := fmt.Sprintf("Arguments:\n\n")
		for a, d := range args {
			argsdesc = fmt.Sprintf("%s%s%s\n", argsdesc, wouter.Wrap(a, width), winner.Wrap(d, width))
		}
		fdescription = fmt.Sprintf("%s\n%s", fdescription, argsdesc)
	}
	return
}

func formatexamples(examples map[string]string) (exdesc string) {
	width := termwidth()
	if examples != nil {
		for d, ex := range examples {
			exdesc = fmt.Sprintf("%s\n%s%s", exdesc, wouter.Wrap(d, width), winner.Wrap(ex, width))
		}
	}
	return
}

var depinfo string

func dependencyInfo(giterr, annexerr error) string {
	if len(depinfo) > 0 {
		return depinfo
	}
	var errmsg string
	if giterr != nil {
		errmsg = fmt.Sprintf("  %s\n", giterr)
	}
	if annexerr != nil {
		errmsg = fmt.Sprintf("%s  %s\n", errmsg, annexerr)
	}

	helppage := "https://gin.g-node.org/G-Node/Info/wiki/GinCli"
	var anchor string
	switch runtime.GOOS {
	case "windows":
		anchor = "#windows"
	case "darwin":
		anchor = "#macos"
	case "linux":
		anchor = "#linux"
	}
	helpurl := fmt.Sprintf("%s%s", helppage, anchor)
	depinfo = fmt.Sprintf("%s  Visit %s for information on installing all the required software\n", errmsg, helpurl)
	return depinfo
}

func disableCommands(cmds map[string]*cobra.Command, giterr, annexerr error) {
	errmsg := "The '%s' command is not available because it requires git and git-annex:"

	errmsg = fmt.Sprintf("%s\n%s", errmsg, dependencyInfo(giterr, annexerr))

	for _, cname := range reqgitannex {
		cmds[cname].Short = fmt.Sprintf("[not available] %s", cmds[cname].Short)
		diemsg := fmt.Sprintf(errmsg, cname)
		cmds[cname].Run = func(c *cobra.Command, args []string) {
			Die(diemsg)
		}
	}

}

// SetUpCommands sets up all the subcommands for the client and returns the root command, ready to execute.
func SetUpCommands(verinfo VersionInfo) *cobra.Command {
	verstr := verinfo.String()
	var rootCmd = &cobra.Command{
		Use:                   "gin",
		Long:                  "GIN Command Line Interface and client for the GIN services", // TODO: Add license and web info
		Version:               fmt.Sprintln(verstr),
		DisableFlagsInUseLine: true,
	}
	cmds := make(map[string]*cobra.Command)

	// Login
	cmds["login"] = LoginCmd()

	// Logout
	cmds["logout"] = LogoutCmd()

	// Add server
	cmds["add-server"] = AddServerCmd()

	// Remove server
	cmds["remove-server"] = RemoveServerCmd()

	// Use server
	cmds["use-server"] = UseServerCmd()

	// Servers
	cmds["servers"] = ServersCmd()

	// Account info
	cmds["info"] = InfoCmd()

	// List repos
	cmds["repos"] = ReposCmd()

	// Repo info
	cmds["repoinfo"] = RepoInfoCmd()

	// Keys
	cmds["keys"] = KeysCmd()

	// Init repo
	cmds["init"] = InitCmd()

	// Add remote
	cmds["add-remote"] = AddRemoteCmd()

	// Remove remote
	cmds["remove-remote"] = RemoveRemoteCmd()

	// Use remote
	cmds["use-remote"] = UseRemoteCmd()

	// Remotes
	cmds["remotes"] = RemotesCmd()

	// Create repo
	cmds["create"] = CreateCmd()

	// Delete repo (unlisted)
	cmds["delete"] = DeleteCmd()

	// Get repo
	cmds["get"] = GetCmd()

	// List files
	cmds["ls"] = LsRepoCmd()

	// Unlock content
	cmds["unlock"] = UnlockCmd()

	// Lock content
	cmds["lock"] = LockCmd()

	// Commit changes
	cmds["commit"] = CommitCmd()

	// Upload
	cmds["upload"] = UploadCmd()

	// Download
	cmds["download"] = DownloadCmd()

	// Sync
	cmds["sync"] = SyncCmd()

	// Get content
	cmds["get-content"] = GetContentCmd()

	// Remove content
	cmds["remove-content"] = RemoveContentCmd()

	// Version
	cmds["version"] = VersionCmd()

	cmds["git"] = GitCmd()

	cmds["annex"] = AnnexCmd()

	// Currently treating git and git-annex dependency together: if one is broken, we assume both are
	// This might change in the future (a command might work with git even if annex isn't found)
	gitok, giterr := verinfo.GitOK()
	annexok, annexerr := verinfo.AnnexOK()

	if !(gitok && annexok) {
		disableCommands(cmds, giterr, annexerr)
		warnmsg := "Some commands are not available:"
		helpTemplate = fmt.Sprintf("%s\n%s\n%s", helpTemplate, warnmsg, dependencyInfo(giterr, annexerr))
	}

	cobra.AddTemplateFunc("wrappedFlagUsages", wrappedFlagUsages)
	rootCmd.SetHelpTemplate(helpTemplate)
	rootCmd.SetUsageTemplate(usageTemplate)

	for _, cmd := range cmds {
		rootCmd.AddCommand(cmd)
	}

	return rootCmd
}
