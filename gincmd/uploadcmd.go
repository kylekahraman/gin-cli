package gincmd

import (
	"fmt"
	"os"

	ginclient "github.com/kylekahraman/gin/ginclient"
	"github.com/kylekahraman/gin/gincmd/ginerrors"
	"github.com/kylekahraman/gin/git"
	"github.com/spf13/cobra"
)

func upload(cmd *cobra.Command, args []string) {
	prStyle := determinePrintStyle(cmd)
	remotes, _ := cmd.Flags().GetStringSlice("to")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	gincl := ginclient.New("gin")
	switch git.Checkwd() {
	case git.NotRepository:
		Die(ginerrors.NotInRepo)
	case git.NotAnnex:
		Warn(ginerrors.MissingAnnex)
	case git.UpgradeRequired:
		annexVersionNotice()
	}

	if _, err := ginclient.DefaultRemote(); err != nil && len(remotes) == 0 {
		Die("upload failed: no remote configured")
	}

	for _, remote := range remotes {
		if remote == allremotes {
			confremotes, err := git.RemoteShow()
			CheckErrorMsg(err, fmt.Sprintf("'all' remotes specified, but could not determine configured remotes: %s", err))
			remotes = make([]string, 0, len(confremotes))
			for r := range confremotes {
				remotes = append(remotes, r)
			}
			break
		}
	}

	if len(remotes) == 0 {
		def, err := ginclient.DefaultRemote()
		if err != nil {
			Die("upload failed: no remote configured")
		}
		remotes = []string{def}
	}

	paths := args

	// NO add/commit phase. Upload is like rsync — it just transfers data.
	// Use `gin commit` separately to stage new files.

	// Count files to upload for each remote
	totalToUpload := 0
	for _, remote := range remotes {
		count, err := git.AnnexCountMissing(paths, remote)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: could not count files for remote %q: %s\n", remote, err)
			continue
		}
		totalToUpload += count
	}

	if prStyle != psJSON {
		if totalToUpload == 0 {
			fmt.Println(":: Uploading — all files already on remote, nothing to do")
			return
		}
		fmt.Printf(":: Uploading to %v: %d file(s) to transfer", remotes, totalToUpload)
		fmt.Println()
	}

	if dryRun {
		fmt.Printf("   Dry run: %d file(s) would be uploaded\n", totalToUpload)
		return
	}

	uploadchan := make(chan git.RepoFileStatus)
	go gincl.Upload(paths, remotes, uploadchan)

	switch prStyle {
	case psJSON:
		formatOutput(uploadchan, prStyle, 0)
	default:
		printUploadProgress(uploadchan, totalToUpload)
	}
}

func UploadCmd() *cobra.Command {
	description := `Upload changes made in a local repository clone to the remote repository on the GIN server. This command must be called from within the local repository clone. Specific files or directories may be specified. All changes made will be sent to the server, including addition of new files, modifications and renaming of existing files, and file deletions.

You can specify which remotes the data will be uploaded to using the --to flag. The flag can be specified multiple times. If the keyword 'all' is specified as a remote, the data is uploaded to all configured remotes.

If no arguments are specified, only changes to files already being tracked are uploaded.`

	args := map[string]string{"<filenames>": "One or more directories or files to upload and update."}
	examples := map[string]string{
		"Upload 'data1.dat' and 'values.csv' to default remote":             "$ gin upload data1.dat values.csv",
		"Upload all files in current directory to default remote":           "$ gin upload .",
		"Upload all previously committed changes to remote named 'labdata'": "$ gin upload --to labdata",
		"Upload all '.zip' files to remotes named 'gin' and 'labdata'":      "$ gin upload --to gin --to labdata *.zip\n    or\n$ gin upload --to gin,labdata *.zip",
	}
	var cmd = &cobra.Command{
		Use:                   "upload [--json] [--dry-run] [--to <remote>] [<filenames>]...",
		Short:                 "Upload local changes to a remote repository",
		Long:                  formatdesc(description, args),
		Args:                  cobra.ArbitraryArgs,
		Example:               formatexamples(examples),
		Run:                   upload,
		DisableFlagsInUseLine: true,
	}
	cmd.Flags().Bool("json", false, jsonHelpMsg)
	cmd.Flags().Bool("dry-run", false, "Show how many files would be uploaded without actually uploading")
	cmd.Flags().StringSliceP("to", "t", nil, "Upload to specific `remote`. Supports multiple remotes, either by specifying multiple times or as a comma separated list (see Examples). If the keyword 'all' is specified, the data is uploaded to all configured remotes.")
	return cmd
}
