package postgres

import (
	"crypto/rand"
	"os"
	"testing"

	"github.com/DATA-DOG/go-txdb"
	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/jinzhu/gorm"
	"github.com/lib/pq"
	"github.com/oklog/ulid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRouter(t *testing.T) {
	db := os.Getenv("DATABASE_URL")
	if db == "" {
		t.Skip("missing database url, skipping postgres tests")
	}

	txdb.Register("pgtest", "postgres", db)

	dialect, ok := gorm.GetDialect("postgres")
	require.True(t, ok)

	gorm.RegisterDialect("pgtest", dialect)

	accId, err := ulid.New(ulid.Now(), rand.Reader)
	require.NoError(t, err)

	t.Run("register and deregister a service", func(t *testing.T) {
		router, err := NewRouter("pgtest", "servtest")
		require.NoError(t, err)

		defer router.db.Close()

		sa, err := ulid.New(ulid.Now(), rand.Reader)
		require.NoError(t, err)

		var serv wire.ServiceInfo
		serv.ServiceId.ULID = sa
		serv.Type = "test"
		serv.Description = "this is a test service"
		serv.Labels = []string{"env=test"}

		ua, err := ulid.New(ulid.Now(), rand.Reader)
		require.NoError(t, err)

		err = router.RegisterService(accId, ua, &serv)
		require.NoError(t, err)

		var (
			ao  Agent
			sos []*Service
		)

		err = dbx.Check(router.db.Where("id = ?", ua).Where("account_id = ?", accId).First(&ao))
		require.NoError(t, err)

		err = dbx.Check(router.db.Where("agent_id = ?", ao.ID).Find(&sos))
		require.NoError(t, err)

		require.Equal(t, 1, len(sos))

		so := sos[0]

		assert.Equal(t, ua, ao.ID.ULID)
		assert.Equal(t, serv.ServiceId, so.ID)
		assert.Equal(t, serv.Type, so.Type)
		assert.Equal(t, serv.Description, so.Description)
		assert.Equal(t, serv.Labels[0], so.Labels[0])

		err = router.DeregisterService(ua, serv.ServiceId.ULID)
		require.NoError(t, err)

		sos = nil

		err = dbx.Check(router.db.Where("agent_id = ?", ao.ID).Find(&sos))
		require.NoError(t, err)

		require.Equal(t, 0, len(sos))
	})

	t.Run("lookup a service by label", func(t *testing.T) {
		router, err := NewRouter("pgtest", "lookuptest")
		require.NoError(t, err)

		defer router.db.Close()

		sa, err := ulid.New(ulid.Now(), rand.Reader)
		require.NoError(t, err)

		var serv wire.ServiceInfo
		serv.ServiceId.ULID = sa
		serv.Type = "test"
		serv.Description = "this is a test service"
		serv.Labels = []string{"env=test"}

		ua, err := ulid.New(ulid.Now(), rand.Reader)
		require.NoError(t, err)

		err = router.RegisterService(accId, ua, &serv)
		require.NoError(t, err)

		services, err := router.LookupService([]string{"env=test"})
		require.NoError(t, err)

		require.Equal(t, len(services), 1)

		si := services[0]

		assert.Equal(t, ua, si.Agent)
		assert.Equal(t, &serv, si.Service)
	})

	t.Run("registers, finds, and deregisters a label link", func(t *testing.T) {
		router, err := NewRouter("pgtest", "lookuptest")
		require.NoError(t, err)

		defer router.db.Close()

		var (
			target = []string{":target=blah.com"}
			dest   = []string{"env=prod"}
		)

		ua, err := ulid.New(ulid.Now(), rand.Reader)
		require.NoError(t, err)

		err = router.RegisterLabelLink(ua, target, dest)
		require.NoError(t, err)

		var link LabelLink

		err = dbx.Check(router.db.Where("account_id = ?", ua).Where("labels = ?", pq.StringArray(target)).First(&link))
		require.NoError(t, err)

		assert.Equal(t, dest, []string(link.Target))

		labels, err := router.FindLabelLink(ua, target)
		require.NoError(t, err)

		assert.Equal(t, dest, labels)

		err = router.DeregisterLabelLink(ua, target)
		require.NoError(t, err)

		link.Target = nil

		err = dbx.Check(router.db.Where("account_id = ?", ua).Where("labels = ?", pq.StringArray(target)).First(&link))
		require.Error(t, err)

		assert.Nil(t, link.Target)
	})

	t.Run("tracks all hostnames that the cluster is handling", func(t *testing.T) {
		router, err := NewRouter("pgtest", "lookuptest")
		require.NoError(t, err)

		defer router.db.Close()

		name := "blah.com"

		ua, err := ulid.New(ulid.Now(), rand.Reader)
		require.NoError(t, err)

		err = router.CreateHostname(ua, name)
		require.NoError(t, err)

		var dom Hostname

		err = dbx.Check(router.db.Where("account_id = ?", ua).Where("name = ?", name).First(&dom))
		require.NoError(t, err)

		assert.Equal(t, name, dom.Name)

		err = router.DeleteHostname(ua, name)
		require.NoError(t, err)

		err = dbx.Check(router.db.Where("account_id = ?", ua).Where("name = ?", name).First(&dom))
		require.Error(t, err)
	})
}
