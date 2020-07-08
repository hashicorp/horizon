package testutils

import (
	"github.com/hashicorp/horizon/pkg/utils"
	"github.com/hashicorp/vault/api"
)

const DefaultTestRootId = utils.DefaultTestRootId

func SetupVault() *api.Client {
	return utils.SetupVault()
}
