package workq

import (
	"database/sql"
	"time"

	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/jinzhu/gorm"
)

type Injector struct {
	db *gorm.DB
}

func (i *Injector) Inject(job *Job) error {
	if job.Id == nil {
		job.Id = pb.NewULID().Bytes()
	}

	tx := i.db.Begin()

	tx.Create(&job)

	tx.Exec("NOTIFY " + listenChannel)

	return dbx.Check(tx.Commit())
}

func (i *Injector) AddPeriodicJob(name, queue, payload string, period time.Duration) error {
	var pjob PeriodicJob

	pjob.Name = name
	pjob.Queue = queue
	pjob.Period = period.String()
	pjob.Payload = payload
	pjob.NextRun = time.Now().Add(period)

	err := dbx.Check(
		i.db.Set("gorm:insert_option",
			"ON CONFLICT (name) DO UPDATE SET queue=EXCLUDED.queue, payload=EXCLUDED.payload, period=EXCLUDED.period, next_run=LEAST(periodic_jobs.next_run, EXCLUDED.next_run)").
			Create(&pjob),
	)

	if err == sql.ErrNoRows {
		return nil
	}

	return err
}
