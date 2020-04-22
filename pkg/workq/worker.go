package workq

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/horizon/pkg/ctxlog"
	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/jinzhu/gorm"
	"github.com/lib/pq"
)

const (
	DefaultPopInterval = time.Minute
	DefaultConcurrency = 5
)

type Worker struct {
	db     *gorm.DB
	queues []string

	Validate func(job *Job) (bool, error)

	Stats struct {
		ListenWakeups int64
	}
}

func NewWorker(db *gorm.DB, queues []string) *Worker {
	return &Worker{db: db, queues: queues}
}

type RunningJob struct {
	Job
	tx *gorm.DB
}

func (r *RunningJob) Abort() error {
	if r.tx == nil {
		return nil
	}

	err := dbx.Check(r.tx.Rollback())
	r.tx = nil
	return err
}

func (r *RunningJob) Close() error {
	if r.tx == nil {
		return nil
	}

	err := dbx.Check(r.tx.Commit())
	r.tx = nil
	return err
}

func (w *Worker) Pop() (*RunningJob, error) {
	tx := w.db.Begin()

	var job RunningJob

	err := dbx.Check(
		tx.
			Set("gorm:query_option", "FOR UPDATE SKIP LOCKED").
			Where("status = ?", "queued").
			Where("queue IN (?)", w.queues).
			First(&job.Job),
	)

	if err != nil {
		tx.Rollback()
		return nil, err
	}

	if w.Validate != nil {
		ok, err := w.Validate(&job.Job)
		if err != nil {
			tx.Rollback()
			return nil, err
		}

		if !ok {
			tx.Rollback()
			return nil, gorm.ErrRecordNotFound
		}
	}

	err = dbx.Check(tx.Model(&job.Job).Update("status", "finished"))
	if err != nil {
		return nil, err
	}

	job.tx = tx

	return &job, nil
}

// Cleanup all the finished jobs
func (w *Worker) CleanupFinished(lag bool) error {
	var query string

	if lag {
		query = "DELETE FROM jobs WHERE status = 'finished' AND created_at + '1 hour'::interval < now()"
	} else {
		query = "DELETE FROM jobs WHERE status = 'finished'"
	}

	return dbx.Check(w.db.Exec(query))
}

type RunConfig struct {
	ConnInfo    string
	PopInterval time.Duration
	Concurrency int
	Handler     func(j *Job) error
}

const listenChannel = "work_available"

// Setup a pq listener and watch for events (and still pop every once in a while)"
func (w *Worker) Run(ctx context.Context, cfg RunConfig) error {
	L := ctxlog.L(ctx)

	reportProblem := func(ev pq.ListenerEventType, err error) {
		if err != nil {
			L.Error("problem observed while listen on postgres channel", "error", err)
		}
	}

	if cfg.Handler == nil {
		return fmt.Errorf("specify a handler to invoke for each job")
	}

	if cfg.PopInterval == 0 {
		cfg.PopInterval = DefaultPopInterval
	}

	if cfg.Concurrency == 1 {
		cfg.Concurrency = DefaultConcurrency
	}

	if cfg.Handler == nil {
		if GlobalRegistry.Size() == 0 {
			return fmt.Errorf("no handler and default registry is empty")
		}
		cfg.Handler = GlobalRegistry.Handle
	}

	minReconn := 10 * time.Second
	maxReconn := time.Minute
	listener := pq.NewListener(cfg.ConnInfo, minReconn, maxReconn, reportProblem)
	defer listener.Close()

	err := listener.Listen(listenChannel)
	if err != nil {
		return err
	}

	ticker := time.NewTicker(cfg.PopInterval)
	defer ticker.Stop()

	workChan := make(chan *RunningJob)

	for i := 0; i < cfg.Concurrency; i++ {
		go w.processJobs(ctx, workChan, cfg.Handler)
	}

	pticker := time.NewTicker(time.Minute)
	defer pticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-pticker.C:
			err := w.CheckPeriodic()
			if err != nil {
				L.Error("error checking periodic jobs", "error", err)
			}
		case <-listener.Notify:
			w.Stats.ListenWakeups++
			// got event
		case <-ticker.C:
			// timed out, try to pop
		}

		job, err := w.Pop()
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				continue
			}

			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case workChan <- job:
			// ok
		}
	}
}

func (w *Worker) processJobs(ctx context.Context, wc chan *RunningJob, f func(*Job) error) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-wc:
			func() {
				defer job.Abort()

				err := f(&job.Job)
				if err == nil {
					job.Close()
				}
			}()
		}
	}
}
