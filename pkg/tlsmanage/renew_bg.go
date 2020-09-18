package tlsmanage

import (
	"context"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/workq"
)

var (
	HubCertRenewPeriod = time.Hour * 24 * 30 // every 30 days
)

func init() {
	workq.RegisterPeriodicJob("renew-hub-cert", "default", "renew-hub-cert", nil, HubCertRenewPeriod)
}

func (m *Manager) RegisterRenewHandler(L hclog.Logger, reg *workq.Registry) {
	reg.Register("renew-hub-cert", func(ctx context.Context, jobType string, _ *struct{}) error {
		err := m.SetupHubCert(ctx)
		if err != nil {
			L.Error("error retrieving updated cert/key for hub", "error", err)
			return err
		}

		err = m.StoreInVault()
		if err != nil {
			L.Error("error storing new cert/key in vault", "error", err)
			return err
		}

		return nil
	})
}
