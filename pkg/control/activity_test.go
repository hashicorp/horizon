package control

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jinzhu/gorm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActivity(t *testing.T) {
	connect := os.Getenv("DATABASE_URL")
	if connect == "" {
		t.Skip("missing database url, skipping postgres tests")
	}

	t.Run("reader can return new log events", func(t *testing.T) {
		db, err := gorm.Open("postgres", connect)
		require.NoError(t, err)

		defer db.Close()

		defer db.Exec("TRUNCATE activity_logs")

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		ar, err := NewActivityReader(ctx, "postgres", connect)
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
}
