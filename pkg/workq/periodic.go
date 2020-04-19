package workq

import (
	"time"

	"github.com/hashicorp/horizon/pkg/dbx"
)

type PeriodicJob struct {
	Id      int `gorm:"primary_key"`
	Queue   string
	Payload string
	Period  string
	NextRun time.Time

	CreatedAt time.Time
}

func (w *Worker) CheckPeriodic() error {
	tx := w.db.Begin()

	var pjob PeriodicJob

	err := dbx.Check(
		tx.
			Set("gorm:query_option", "FOR UPDATE SKIP LOCKED").
			Where("next_run <= now()").
			First(&pjob),
	)

	if err != nil {
		tx.Rollback()
		return err
	}

	dur, err := time.ParseDuration(pjob.Period)
	if err != nil {
		tx.Rollback()
		return err
	}

	tx.Model(&pjob).Update("next_run", time.Now().Add(dur))

	job := NewJob()
	job.Queue = pjob.Queue
	job.Payload = pjob.Payload

	tx.Create(&job)

	return dbx.Check(tx.Commit())
}
