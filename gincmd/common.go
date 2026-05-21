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

// printUploadProgress displays an rsync-style live progress view for uploads.
// Shows current file progress, a file-level progress bar with ETA, and
// handles resumed/skipped detection.
func printUploadProgress(statuschan <-chan git.RepoFileStatus, totalFiles int) (filesuccess map[string]bool) {
	filesuccess = make(map[string]bool)
	if totalFiles <= 0 {
		fmt.Println("   Nothing to upload — all files already on remote")
		return
	}

	ndigits := len(fmt.Sprintf("%d", totalFiles))
	dfmt := fmt.Sprintf("%%%dd/%%%dd", ndigits, ndigits)

	// Track state per file
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
	seen := make(map[string]*fileState)

	var (
		completed    int
		skipped      int
		failed       int
		startTime    time.Time
		overallBytes int64
		firstData    bool
	)

	// Track the currently-displayed line length for \r clearing
	lastLineLen := 0

	fmtbytesFn := func(b int64) string {
		if b < 0 {
			return "0 B"
		}
		return humanize.IBytes(uint64(b))
	}

	etaFn := func(remBytes int64, rate float64) string {
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
		secsRem := int(secs) % 60
		if mins < 60 {
			return fmt.Sprintf("%dm %ds", mins, secsRem)
		}
		hours := mins / 60
		mins = mins % 60
		return fmt.Sprintf("%dh %dm", hours, mins)
	}

	// Build a display closure we can call from both stat events and ticker
	renderDisplay := func(fname string, fs *fileState, elapsed time.Duration, compl int) {
		// Part 1: current file info
		filePart := ""
		if fname != "" && fs != nil {
			// Clean up filename: strip " (version: ...)" suffix added by gin-cli
			showname := fname
			if idx := strings.Index(showname, " (version: "); idx > 0 {
				showname = showname[:idx]
			}
			// Shorten if too long
			if len(showname) > 25 {
				showname = "..." + showname[len(showname)-22:]
			}

			if fs.done {
				filePart = fmt.Sprintf("%s  %s  %s", cyan(showname), green("✔"), fmtbytesFn(int64(fs.totalSize)))
			} else if fs.skipped {
				filePart = fmt.Sprintf("%s  %s", cyan(showname), yellow("⏭ already on remote"))
			} else if fs.failed {
				filePart = fmt.Sprintf("%s  %s", cyan(showname), red("✖"))
			} else if fs.lastPct != "" && fs.totalSize > 0 && fs.lastBytes > 0 {
				pctVal := float64(fs.lastBytes) / float64(fs.totalSize) * 100
				barW := 12
				filled := int(pctVal / 100 * float64(barW))
				if filled > barW {
					filled = barW
				}
				bar := strings.Repeat("█", filled) + strings.Repeat("░", barW-filled)
				eta := ""
				rateVal := parseRate(fs.rate)
				if rateVal > 0 {
					remBytes := int64(fs.totalSize) - int64(fs.lastBytes)
					if remBytes > 0 {
						eta = " " + etaFn(remBytes, rateVal)
					}
				}
				filePart = fmt.Sprintf("%s [%s] %s/%s  %s%s",
					cyan(showname), bar,
					fmtbytesFn(int64(fs.lastBytes)), fmtbytesFn(int64(fs.totalSize)),
					fs.rate, eta)
			} else if fs.totalSize > 0 && fs.lastBytes == 0 {
				filePart = fmt.Sprintf("%s  %s/%s  %s",
					cyan(showname),
					fmtbytesFn(int64(fs.lastBytes)), fmtbytesFn(int64(fs.totalSize)),
					fs.rate)
			} else if fs.lastPct != "" {
				filePart = fmt.Sprintf("%s  %s%%  %s",
					cyan(showname), fs.lastPct, fs.rate)
			}
		}

		// Part 2: overall progress bar with percentage
		barwidth := 20
		fill := 0
		overallPct := 0.0
		if totalFiles > 0 {
			overallPct = float64(compl) / float64(totalFiles) * 100
			fill = int(overallPct / 100 * float64(barwidth))
			if fill < 0 {
				fill = 0
			}
			if fill > barwidth {
				fill = barwidth
			}
		}
		barStr := fmt.Sprintf(dfmt, compl, totalFiles)
		pctStr := fmt.Sprintf("(%d%%)", int(overallPct))
		overallPart := fmt.Sprintf(" [%s%s] %s  %s", strings.Repeat("=", fill), strings.Repeat(" ", barwidth-fill), barStr, pctStr)

		if skipped > 0 {
			overallPart += yellow(fmt.Sprintf(" (%d skipped)", skipped))
		}
		if failed > 0 {
			overallPart += red(fmt.Sprintf(" (%d failed)", failed))
		}

		overallRate := 0.0
		if elapsed.Seconds() > 0 && overallBytes > 0 {
			overallRate = float64(overallBytes) / elapsed.Seconds()
		}
		if overallRate <= 0 && fs != nil && fs.rate != "" {
			overallRate = parseRate(fs.rate)
		}

		if overallRate > 0 {
			overallPart += fmt.Sprintf("  %s/s", humanize.IBytes(uint64(overallRate)))
			remaining := totalFiles - compl
			if remaining < 0 {
				remaining = 0
			}
			if remaining > 0 {
				avgBytes := int64(0)
				if completed > 0 {
					avgBytes = overallBytes / int64(completed)
				}
				remBytes := avgBytes * int64(remaining)
				eta := etaFn(remBytes, overallRate)
				if eta != "" {
					overallPart += fmt.Sprintf("  ETA %s", eta)
				}
			}
		}

		// Add elapsed time
		timeStr := ""
		if elapsed.Seconds() > 0 {
			secs := int(elapsed.Seconds())
			if secs < 60 {
				timeStr = fmt.Sprintf(" [%ds]", secs)
			} else if secs < 3600 {
				timeStr = fmt.Sprintf(" [%dm %ds]", secs/60, secs%60)
			} else {
				timeStr = fmt.Sprintf(" [%dh %dm]", secs/3600, (secs%3600)/60)
			}
			overallPart += cyan(timeStr)
		}

		line := ""
		if filePart != "" && overallPart != "" {
			line = filePart + " ||" + overallPart
		} else if filePart != "" {
			line = filePart
		} else {
			line = overallPart
		}

		if lastLineLen > 0 {
			fmt.Printf("\r%s\r", strings.Repeat(" ", lastLineLen))
		}
		fmt.Print("\r" + line)
		lastLineLen = len(line)
	}

	// Main event loop: process status updates with periodic alive-signal refresh
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	var (
		lastFname  string
		lastFs     *fileState
		waitingMsg string
	)
	for {
		select {
		case stat, ok := <-statuschan:
			if !ok {
				// Channel closed — upload done
				renderDisplay(lastFname, lastFs, time.Since(startTime), completed+skipped+failed)
				fmt.Println()
				if completed == 0 && skipped == 0 && failed == 0 {
					fmt.Println("   Nothing to upload")
				}
				return
			}

			if !firstData {
				startTime = time.Now()
				firstData = true
				waitingMsg = ""
			}

			fname := stat.FileName
			lastFname = fname

			fs, ok := seen[fname]
			if !ok {
				fs = &fileState{}
				seen[fname] = fs
			}
			lastFs = fs

			if stat.Progress == "100%" {
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
			} else if stat.Progress == "skipped" {
				if !fs.done && !fs.skipped {
					fs.skipped = true
					fs.note = stat.Note
					skipped++
					filesuccess[fname] = true
				}
			} else if stat.Err != nil {
				if !fs.failed {
					fs.failed = true
					fs.note = stat.Err.Error()
					failed++
					filesuccess[fname] = false
				}
			} else {
				// Progress update
				fs.lastPct = stat.Progress
				fs.lastBytes = stat.ByteProgress
				fs.totalSize = stat.TotalSize
				fs.rate = stat.Rate
				fs.note = stat.Note
			}

			compl := completed + skipped + failed
			renderDisplay(fname, fs, time.Since(startTime), compl)

		case <-ticker.C:
			if !firstData {
				// Still waiting for first status — show alive indicator
				waitingTime := time.Since(startTime)
				if startTime.IsZero() {
					startTime = time.Now()
				}
				secs := int(waitingTime.Seconds())
				// Animate dots
				dots := strings.Repeat(".", (secs%3)+1)
				waitingMsg = fmt.Sprintf("\r   Preparing...%s  (%ds)", dots, secs)
				if lastLineLen > 0 {
					fmt.Printf("\r%s\r", strings.Repeat(" ", lastLineLen))
				}
				fmt.Print(yellow(waitingMsg))
				lastLineLen = len(waitingMsg)
			} else {
				// Have data — refresh the display with updated elapsed time
				compl := completed + skipped + failed
				if compl < totalFiles {
					renderDisplay(lastFname, lastFs, time.Since(startTime), compl)
				}
			}
		}
	}
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
