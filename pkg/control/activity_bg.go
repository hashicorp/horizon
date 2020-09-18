package control

import (
	context "context"

	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/jinzhu/gorm"
)

var LogPruneInterval = "6 hours"

type LogCleaner struct {
	DB *gorm.DB
}

func (l *LogCleaner) CleanupActivityLog(ctx context.Context, jobType string, _ *struct{}) error {
	return dbx.Check(
		l.DB.Exec("DELETE FROM activity_logs WHERE created_at < now() - ?::interval", LogPruneInterval),
	)
}
