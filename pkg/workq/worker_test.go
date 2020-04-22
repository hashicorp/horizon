package workq

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/jinzhu/gorm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorker(t *testing.T) {
	connect := os.Getenv("DATABASE_URL")
	if connect == "" {
		t.Skip("missing database url, skipping postgres tests")
	}

	db, err := gorm.Open("postgres", connect)
	require.NoError(t, err)

	defer db.Close()

	t.Run("fetches an available job", func(t *testing.T) {
		db.Exec("TRUNCATE jobs")

		tx := db.Begin()

		job := NewJob()
		job.Queue = "a"

		job.Set("test", 1)

		err := dbx.Check(tx.Create(&job))
		require.NoError(t, err)

		err = dbx.Check(tx.Commit())
		require.NoError(t, err)

		w := NewWorker(db, []string{"a"})

		j2, err := w.Pop()
		require.NoError(t, err)

		assert.Equal(t, job.Id, j2.Id)

		var j3 Job

		// Check we can't see it at all here.
		err = dbx.Check(db.Where("status = ?", "finished").First(&j3))
		require.Error(t, err)

		j2.Close()

		err = dbx.Check(db.Where("status = ?", "finished").First(&j3))
		require.NoError(t, err)

		assert.Equal(t, job.Id, j3.Id)
	})

	t.Run("skips jobs being run by other workers", func(t *testing.T) {
		db.Exec("TRUNCATE jobs")

		tx := db.Begin()

		job := NewJob()
		job.Queue = "a"

		job.Set("test", 1)

		err := dbx.Check(tx.Create(&job))
		require.NoError(t, err)

		err = dbx.Check(tx.Commit())
		require.NoError(t, err)

		var w1Job, w2Job *Job

		w1 := NewWorker(db, []string{"a"})
		w1.Validate = func(j *Job) (bool, error) {
			w1Job = j
			time.Sleep(2 * time.Second)
			return true, nil
		}

		w2 := NewWorker(db, []string{"a"})
		w2.Validate = func(j *Job) (bool, error) {
			w2Job = j
			time.Sleep(2 * time.Second)
			return true, nil
		}

		j2, err := w1.Pop()
		require.NoError(t, err)

		defer j2.Close()

		var (
			j21  *RunningJob
			err2 error
			wg   sync.WaitGroup
		)

		wg.Add(1)
		go func() {
			defer wg.Done()
			j21, err2 = w2.Pop()
		}()

		wg.Wait()
		assert.Equal(t, job.Id, j2.Id)

		if j21 != nil {
			defer j21.Close()
		}

		j2.Close()

		var j3 Job

		err = dbx.Check(db.Where("status = ?", "finished").First(&j3))
		require.NoError(t, err)

		assert.Equal(t, job.Id, j3.Id)

		assert.Equal(t, job.Id, w1Job.Id)

		assert.Nil(t, w2Job)
		assert.Nil(t, j21)
		assert.Equal(t, err2, gorm.ErrRecordNotFound)
	})

	t.Run("picks a unique set of jobs per worker", func(t *testing.T) {
		db.Exec("TRUNCATE jobs")

		tx := db.Begin()

		job := NewJob()
		job.Queue = "a"

		job.Set("test", 1)

		err := dbx.Check(tx.Create(&job))
		require.NoError(t, err)

		job1 := NewJob()
		job1.Queue = "a"

		job1.Set("test", 1)

		err = dbx.Check(tx.Create(&job1))
		require.NoError(t, err)

		err = dbx.Check(tx.Commit())
		require.NoError(t, err)

		var w1Job, w2Job *Job

		w1 := NewWorker(db, []string{"a"})
		w1.Validate = func(j *Job) (bool, error) {
			w1Job = j
			time.Sleep(2 * time.Second)
			return true, nil
		}

		w2 := NewWorker(db, []string{"a"})
		w2.Validate = func(j *Job) (bool, error) {
			w2Job = j
			time.Sleep(2 * time.Second)
			return true, nil
		}

		j2, err := w1.Pop()
		require.NoError(t, err)

		defer j2.Close()

		var (
			j21  *RunningJob
			err2 error
			wg   sync.WaitGroup
		)

		wg.Add(1)
		go func() {
			defer wg.Done()
			j21, err2 = w2.Pop()
		}()

		wg.Wait()

		if j21 != nil {
			defer j21.Close()
		}

		require.NoError(t, err2)

		assert.Equal(t, job.Id, j2.Id)

		assert.Equal(t, job.Id, w1Job.Id)

		assert.Equal(t, job1.Id, j21.Id)
		assert.Equal(t, job1.Id, w2Job.Id)
	})

	t.Run("picks up failed jobs by next worker", func(t *testing.T) {
		db.Exec("TRUNCATE jobs")

		tx := db.Begin()

		job := NewJob()
		job.Queue = "a"

		job.Set("test", 1)

		err := dbx.Check(tx.Create(&job))
		require.NoError(t, err)

		err = dbx.Check(tx.Commit())
		require.NoError(t, err)

		w1 := NewWorker(db, []string{"a"})

		w2 := NewWorker(db, []string{"a"})

		j2, err := w1.Pop()
		require.NoError(t, err)

		j2.Abort()

		j21, err := w2.Pop()

		defer j21.Close()

		require.NoError(t, err)

		assert.Equal(t, job.Id, j2.Id)
		assert.Equal(t, job.Id, j21.Id)
	})

	t.Run("cleans up finished jobs", func(t *testing.T) {
		db.Exec("TRUNCATE jobs")

		tx := db.Begin()

		job := NewJob()
		job.Queue = "a"

		job.Set("test", 1)

		err := dbx.Check(tx.Create(&job))
		require.NoError(t, err)

		err = dbx.Check(tx.Commit())
		require.NoError(t, err)

		w := NewWorker(db, []string{"a"})

		j2, err := w.Pop()
		require.NoError(t, err)

		j2.Close()

		var j3 Job

		err = dbx.Check(db.Where("status = ?", "finished").First(&j3))
		require.NoError(t, err)

		assert.Equal(t, job.Id, j3.Id)

		err = w.CleanupFinished(false)
		require.NoError(t, err)

		err = dbx.Check(db.Where("status = ?", "finished").First(&j3))
		require.Error(t, err)

		job = NewJob()
		job.Queue = "a"
		job.Status = "finished"

		job.Set("test", 1)

		err = dbx.Check(db.Create(&job))
		require.NoError(t, err)

		j3 = Job{}

		err = dbx.Check(db.Where("status = ?", "finished").First(&j3))
		require.NoError(t, err)

		err = w.CleanupFinished(true)
		require.NoError(t, err)

		j3 = Job{}

		err = dbx.Check(db.Where("status = ?", "finished").First(&j3))
		require.NoError(t, err)

		db.Model(&job).Update("created_at", time.Now().Add(-6*time.Hour))

		err = w.CleanupFinished(true)
		require.NoError(t, err)

		j3 = Job{}

		err = dbx.Check(db.Where("status = ?", "finished").First(&j3))
		require.Error(t, err)
	})

	t.Run("respects the queue on a job", func(t *testing.T) {
		db.Exec("TRUNCATE jobs")

		tx := db.Begin()

		job := NewJob()
		job.Queue = "b"

		job.Set("test", 1)

		err := dbx.Check(tx.Create(&job))
		require.NoError(t, err)

		err = dbx.Check(tx.Commit())
		require.NoError(t, err)

		w := NewWorker(db, []string{"a"})

		j2, err := w.Pop()
		require.Error(t, err)

		job = NewJob()
		job.Queue = "a"

		job.Set("test", 1)

		err = dbx.Check(db.Create(&job))
		require.NoError(t, err)

		j2, err = w.Pop()
		require.NoError(t, err)

		defer j2.Close()

		assert.Equal(t, job.Id, j2.Id)

		var j3 Job

		// Check we can't see it at all here.
		err = dbx.Check(db.Where("status = ?", "finished").First(&j3))
		require.Error(t, err)

		j2.Close()

		err = dbx.Check(db.Where("status = ?", "finished").First(&j3))
		require.NoError(t, err)

		assert.Equal(t, job.Id, j3.Id)
	})

	t.Run("invokes a handler using LISTEN", func(t *testing.T) {
		db.Exec("TRUNCATE jobs")

		w := NewWorker(db, []string{"a"})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var jobs []*Job

		go w.Run(ctx, RunConfig{
			ConnInfo:    connect,
			PopInterval: time.Minute,
			Concurrency: 1,
			Handler: func(j *Job) error {
				jobs = append(jobs, j)
				return nil
			},
		})

		time.Sleep(time.Second)

		var i Injector
		i.db = db

		job := NewJob()
		job.Queue = "a"

		job.Set("test", 1)

		err = i.Inject(job)
		require.NoError(t, err)

		time.Sleep(time.Second)

		require.Equal(t, 1, len(jobs))
		assert.Equal(t, job.Id, jobs[0].Id)

		assert.Equal(t, int64(1), w.Stats.ListenWakeups)
	})
}
