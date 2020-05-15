package workq

import (
	"time"

	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/jinzhu/gorm"
)

type PeriodicJob struct {
	Id      int `gorm:"primary_key"`
	Name    string
	Queue   string
	JobType string
	Payload []byte
	Period  string
	NextRun time.Time

	CreatedAt time.Time
}

func (w *Worker) CheckPeriodic() error {
	// We churn this loop until there are no more periodic jobs to schedule
	for {
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
			if err == gorm.ErrRecordNotFound {
				return nil
			}

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
		job.JobType = pjob.JobType

		tx.Create(&job)

		w.L.Info("queued job via periodic job", "name", pjob.Name, "queue", pjob.Queue, "job-type", pjob.JobType)

		err = dbx.Check(tx.Commit())
		if err != nil {
			return err
		}
	}
}
