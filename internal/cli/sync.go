package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/ansonnchan/tokenholic/internal/autosync"
	"github.com/ansonnchan/tokenholic/internal/numfmt"
	usageSync "github.com/ansonnchan/tokenholic/internal/sync"
)

const credentialRefreshWindow = 7 * 24 * time.Hour

// scanTimeout bounds the local scan before an upload. The upload itself has
// no overall limit: a first upload of a long history is many batches, each
// one bounded, and cutting the run off midway only made the user run it
// again.
const scanTimeout = 5 * time.Minute

// unfinishedUpload is an upload that stopped after the server had accepted
// part of it. What was accepted is committed locally and is not sent again.
type unfinishedUpload struct {
	uploaded int64
	pending  int
	cause    error
}

func (e *unfinishedUpload) Error() string {
	return fmt.Sprintf("upload stopped after %d of %d buckets: %v", e.uploaded, e.pending, e.cause)
}

func (e *unfinishedUpload) Unwrap() error { return e.cause }

// reportUpload writes an upload's progress, one line a batch. A pass of a
// single batch says nothing until it is done.
func reportUpload(output io.Writer) func(usageSync.Progress) {
	return func(progress usageSync.Progress) {
		switch {
		case progress.Batches < 2:
		case progress.Batch == 0:
			fmt.Fprintf(output, "Uploading %s buckets in %d batches. A long history can take several minutes.\n", numfmt.Exact(uint64(progress.Pending)), progress.Batches)
			fmt.Fprintln(output, "Stopping is safe: what is uploaded is kept, and `tokenholic sync` continues from there.")
		default:
			fmt.Fprintf(output, "  %s of %s uploaded\n", numfmt.Exact(uint64(progress.Acknowledged)), numfmt.Exact(uint64(progress.Pending)))
		}
	}
}

func synchronizeLocal(ctx context.Context, home, codexHome, apiURL string, dryRun bool, output io.Writer) (err error) {
	lock, err := autosync.AcquireDefault()
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := lock.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	database, application, err := openLocalApplication(ctx, home, codexHome)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), localCloseTimeout)
		defer cancel()
		if closeErr := database.Close(closeCtx); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	scanCtx, cancelScan := context.WithTimeout(ctx, scanTimeout)
	err = application.Refresh(scanCtx)
	cancelScan()
	if err != nil {
		return err
	}
	if dryRun {
		_, err := usageSync.DryRun(ctx, database, output)
		return err
	}
	summary, err := uploadPending(ctx, database, apiURL, reportUpload(output))
	if err != nil {
		if summary.Acknowledged > 0 {
			return &unfinishedUpload{uploaded: summary.Acknowledged, pending: summary.Pending, cause: err}
		}
		return err
	}
	fmt.Fprintf(output, "Synchronized %d buckets in %d batches.\n", summary.Acknowledged, summary.Batches)
	return nil
}
