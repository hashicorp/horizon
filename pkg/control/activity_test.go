package control

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/horizon/internal/testsql"
	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActivity(t *testing.T) {
	const testDbName = "hzn_control"

	t.Run("reader can return new log events", func(t *testing.T) {
		db := testsql.TestPostgresDB(t, testDbName)
		defer db.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		ar, err := NewActivityReader(ctx, "postgres",
			testsql.TestPostgresDBString(t, testDbName))
		require.NoError(t, err)

		defer ar.Close()

		time.Sleep(time.Second)

		ai, err := NewActivityInjector(db)
		require.NoError(t, err)

		err = ai.Inject(ctx, []byte(`"this is an event"`))
		require.NoError(t, err)

		select {
		case <-ctx.Done():
			require.NoError(t, ctx.Err())
		case entries := <-ar.C:
			assert.Equal(t, []byte(`"this is an event"`), entries[0].Event)
		}

		err = ai.Inject(ctx, []byte(`"this is a second event"`))
		require.NoError(t, err)

		select {
		case <-ctx.Done():
			require.NoError(t, ctx.Err())
		case entries := <-ar.C:
			assert.Equal(t, []byte(`"this is a second event"`), entries[0].Event)
		}
	})

	t.Run("prunes old logs", func(t *testing.T) {
		db := testsql.TestPostgresDB(t, testDbName)
		defer db.Close()

		var ae ActivityLog
		ae.CreatedAt = time.Now().Add(-6 * time.Hour)
		ae.Event = []byte(`1`)

		err := dbx.Check(db.Create(&ae))
		require.NoError(t, err)

		var lc LogCleaner
		lc.DB = db
		err = lc.CleanupActivityLog(nil, "cleanup-activity-log", nil)
		require.NoError(t, err)

		var ae2 ActivityLog
		err = dbx.Check(db.First(&ae2))
		require.Error(t, err)
	})
}
