package workq

import (
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/internal/testsql"
	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPeriodic(t *testing.T) {
	L := hclog.L()

	t.Run("creates jobs from periodic jobs", func(t *testing.T) {
		db := testsql.TestPostgresDB(t, "periodic")
		defer db.Close()

		var pjob PeriodicJob

		pjob.NextRun = time.Now()
		pjob.Queue = "a"
		pjob.Period = "30m"
		pjob.JobType = "test"
		pjob.Payload = []byte("1")

		err := dbx.Check(db.Create(&pjob))
		require.NoError(t, err)

		w := NewWorker(L, db, []string{"a"})

		err = w.CheckPeriodic()
		require.NoError(t, err)

		var job Job

		err = dbx.Check(db.First(&job))
		require.NoError(t, err)

		assert.Equal(t, pjob.Queue, job.Queue)
		assert.Equal(t, pjob.Payload, job.Payload)

		var pjob2 PeriodicJob

		err = dbx.Check(db.First(&pjob2))

		assert.NotEqual(t, pjob.NextRun, pjob2.NextRun)

		err = w.CheckPeriodic()
		require.NoError(t, err)

	})

	t.Run("creates or updates periodic jobs", func(t *testing.T) {
		db := testsql.TestPostgresDB(t, "periodic")
		defer db.Close()

		var i Injector
		i.db = db

		err := i.AddPeriodicJob("foo", "a", "test", "aabbcc", time.Hour)
		require.NoError(t, err)

		var pjob PeriodicJob

		err = dbx.Check(db.First(&pjob))
		require.NoError(t, err)

		err = i.AddPeriodicJob("foo", "a", "test", "aabbccdd", time.Minute)
		require.NoError(t, err)

		var pjob2 PeriodicJob

		err = dbx.Check(db.Last(&pjob2))
		require.NoError(t, err)

		assert.Equal(t, pjob.Id, pjob2.Id)

		assert.Equal(t, pjob2.Payload, []byte(`"aabbccdd"`))
		assert.True(t, pjob2.NextRun.Before(pjob.NextRun))

		err = i.AddPeriodicJob("foo", "a", "test", "aabbccdd", time.Hour)
		require.NoError(t, err)

		var pjob3 PeriodicJob

		err = dbx.Check(db.Last(&pjob3))
		require.NoError(t, err)

		assert.Equal(t, pjob.Id, pjob2.Id)
		assert.True(t, pjob2.NextRun.Equal(pjob3.NextRun))
	})
}
