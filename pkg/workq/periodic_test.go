package workq

import (
	"os"
	"testing"
	"time"

	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/jinzhu/gorm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPeriodic(t *testing.T) {
	connect := os.Getenv("DATABASE_URL")
	if connect == "" {
		t.Skip("missing database url, skipping postgres tests")
	}

	db, err := gorm.Open("postgres", connect)
	require.NoError(t, err)

	defer db.Close()

	t.Run("creates jobs from periodic jobs", func(t *testing.T) {
		db.Exec("TRUNCATE jobs")
		db.Exec("TRUNCATE periodic_jobs")

		var pjob PeriodicJob

		pjob.NextRun = time.Now()
		pjob.Queue = "a"
		pjob.Period = "30m"
		pjob.Payload = "aabbcc"

		err = dbx.Check(db.Create(&pjob))
		require.NoError(t, err)

		w := NewWorker(db, []string{"a"})

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
		require.Error(t, err)

	})
}
