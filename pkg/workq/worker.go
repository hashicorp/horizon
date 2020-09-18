package workq

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/jinzhu/gorm"
	"github.com/lib/pq"
)

const (
	DefaultPopInterval     = time.Minute
	DefaultConcurrency     = 5
	DefaultCleanupInterval = time.Hour
	MaximumAttempts        = 100
)

type Worker struct {
	db     *gorm.DB
	queues []string

	L hclog.Logger

	Validate func(job *Job) (bool, error)

	Stats struct {
		ListenWakeups int64
	}
}

func NewWorker(L hclog.Logger, db *gorm.DB, queues []string) *Worker {
	return &Worker{L: L, db: db, queues: queues}
}

type RunningJob struct {
	Job
	L  hclog.Logger
	tx *gorm.DB
}

var MaxCoolOffDuration = 240 * time.Second

func (r *RunningJob) Abort() error {
	if r.tx == nil {
		return nil
	}

	attempts := r.Job.Attempts + 1

	if attempts >= MaximumAttempts {
		r.L.Error("maximum attempts reached, dropping job",
			"id", pb.ULIDFromBytes(r.Id).SpecString(),
			"queue", r.Queue,
			"job-type", r.JobType,
			"created-at", r.CreatedAt.String(),
		)

		r.tx.Delete(&r.Job)

		return dbx.Check(r.tx.Commit())
	}

	dur := time.Duration(attempts*10) * time.Second

	if dur > MaxCoolOffDuration {
		dur = MaxCoolOffDuration
	}

	cool := time.Now().Add(dur)

	err := dbx.Check(r.tx.Model(&r.Job).
		Updates(map[string]interface{}{
			"status":         "queued",
			"attempts":       attempts,
			"cool_off_until": &cool,
		}),
	)

	if err != nil {
		return err
	}

	err = dbx.Check(r.tx.Commit())
	r.tx = nil
	return err
}

func (r *RunningJob) AbortAndRequeue() error {
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
	job.L = w.L

	w.L.Debug("attempting to pop job from database", "queues", w.queues)

	err := dbx.Check(
		tx.
			Set("gorm:query_option", "FOR UPDATE SKIP LOCKED").
			Where("status = ?", "queued").
			Where("queue IN (?)", w.queues).
			Where("cool_off_until IS NULL or now() >= cool_off_until").
			First(&job.Job),
	)

	if err != nil {
		tx.Rollback()
		return nil, err
	}

	w.L.Debug("job found", "job-type", job.JobType)

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
	ConnInfo     string
	PopInterval  time.Duration
	Concurrency  int
	CleanupCheck time.Duration
	Handler      func(ctx context.Context, j *Job) error
}

const listenChannel = "work_available"

// Setup a pq listener and watch for events (and still pop every once in a while)"
func (w *Worker) Run(ctx context.Context, cfg RunConfig) error {
	L := w.L

	// setup any default periodics
	periodMu.Lock()

	var inj Injector
	inj.db = w.db

	for _, pe := range defaultPeriodics {
		L.Info("added periodic job",
			"name", pe.name,
			"queue", pe.queue,
			"job-type", pe.jobType,
			"period", pe.period,
		)

		inj.AddPeriodicJobRaw(pe.name, pe.queue, pe.jobType, pe.payload, pe.period)
	}

	periodMu.Unlock()

	reportProblem := func(ev pq.ListenerEventType, err error) {
		if err != nil {
			L.Error("problem observed while listen on postgres channel", "error", err)
		}
	}

	if cfg.PopInterval == 0 {
		cfg.PopInterval = DefaultPopInterval
	}

	if cfg.Concurrency == 0 {
		cfg.Concurrency = DefaultConcurrency
	}

	if cfg.CleanupCheck == 0 {
		cfg.CleanupCheck = DefaultCleanupInterval
	}

	if cfg.Handler == nil {
		if GlobalRegistry.Size() == 0 {
			return fmt.Errorf("no handler and default registry is empty")
		}

		GlobalRegistry.PrintHandlers(L)
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

	cticker := time.NewTicker(cfg.CleanupCheck)
	defer cticker.Stop()

	L.Debug("beginning workq run loop")

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-pticker.C:
			L.Debug("checking periodic jobs")
			err := w.CheckPeriodic()
			if err != nil {
				L.Error("error checking periodic jobs", "error", err)
			}

			continue
		case <-cticker.C:
			err := w.CleanupFinished(true)
			if err != nil {
				L.Error("error cleaning up finished jobs", "error", err)
			}

			continue
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

		L.Debug("running job", "job-type", job.JobType)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case workChan <- job:
			// ok
		}
	}
}

func (w *Worker) processJobs(ctx context.Context, wc chan *RunningJob, f func(context.Context, *Job) error) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-wc:
			func() {
				defer job.Abort()

				w.L.Debug("executing job handler", "job-type", job.JobType)
				err := f(ctx, &job.Job)
				if err == nil {
					w.L.Debug("job finished")
					job.Close()
				} else {
					w.L.Error("error executing job function", "error", err, "job-type", job.JobType)
				}
			}()
		}
	}
}
