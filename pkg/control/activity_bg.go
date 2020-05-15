package control

import (
	context "context"
	"time"

	"github.com/hashicorp/horizon/pkg/config"
	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/hashicorp/horizon/pkg/workq"
)

var LogPruneInterval = "6 hours"

func init() {
	workq.RegisterHandler("cleanup-activity-log", cleanupActivityLog)
	workq.RegisterPeriodicJob("cleanup-activity-log", "default", "cleanup-activity-log", 0, time.Hour)
}

func cleanupActivityLog(ctx context.Context, jobType string, noop int) error {
	return dbx.Check(
		config.DB().Exec("DELETE FROM activity_logs WHERE created_at < now() - ?::interval", LogPruneInterval),
	)
}
