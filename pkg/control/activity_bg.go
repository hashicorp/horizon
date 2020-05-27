package control

import (
	context "context"
	"time"

	"github.com/hashicorp/horizon/pkg/config"
	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/hashicorp/horizon/pkg/workq"
	"github.com/jinzhu/gorm"
)

var LogPruneInterval = "6 hours"

func init() {
	lc := &LogCleaner{db: config.DB()}
	workq.RegisterHandler("cleanup-activity-log", lc.CleanupActivityLog)
	workq.RegisterPeriodicJob("cleanup-activity-log", "default", "cleanup-activity-log", 0, time.Hour)
}

type LogCleaner struct {
	db *gorm.DB
}

func (l *LogCleaner) CleanupActivityLog(ctx context.Context, jobType string, noop int) error {
	return dbx.Check(
		l.db.Exec("DELETE FROM activity_logs WHERE created_at < now() - ?::interval", LogPruneInterval),
	)
}
