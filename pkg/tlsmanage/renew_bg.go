package tlsmanage

import (
	"context"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/workq"
)

// TODO: (catsby) change this back
// var hubCertRenewPeriod = time.Hour * 24 * 30 // every 30 days
var hubCertRenewPeriod = time.Hour * 24 // every day

func init() {
	workq.RegisterPeriodicJob("renew-hub-cert", "default", "renew-hub-cert", nil, hubCertRenewPeriod)
}

func (m *Manager) RegisterRenewHandler(L hclog.Logger, reg *workq.Registry) {
	reg.Register("renew-hub-cert", func(ctx context.Context, _ string, _ *struct{}) error {
		L.Warn("renewing hub cert")
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
		L.Warn("done renewing hub cert")

		return nil
	})
}
