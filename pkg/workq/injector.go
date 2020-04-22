package workq

import (
	"database/sql"
	"encoding/json"
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

func (i *Injector) AddPeriodicJob(name, queue, jt string, v interface{}, period time.Duration) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}

	return i.AddPeriodicJobRaw(name, queue, jt, data, period)
}

func (i *Injector) AddPeriodicJobRaw(name, queue, jt string, payload []byte, period time.Duration) error {
	var pjob PeriodicJob

	pjob.Name = name
	pjob.Queue = queue
	pjob.Period = period.String()
	pjob.JobType = jt
	pjob.NextRun = time.Now().Add(period)
	pjob.Payload = payload

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
