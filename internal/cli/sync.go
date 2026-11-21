package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/ansonnchan/tokenholic/internal/autosync"
	usageSync "github.com/ansonnchan/tokenholic/internal/sync"
)

const credentialRefreshWindow = 7 * 24 * time.Hour

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
	if err := application.Refresh(ctx); err != nil {
		return err
	}
	if dryRun {
		_, err := usageSync.DryRun(ctx, database, output)
		return err
	}
	summary, err := uploadPending(ctx, database, apiURL)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "Synchronized %d buckets in %d batches.\n", summary.Acknowledged, summary.Batches)
	return nil
}
