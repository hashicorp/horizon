package testutils

import (
	"os"

	"github.com/hashicorp/vault/api"
)

const DefaultTestRootId = "hznroot"

func SetupVault() *api.Client {
	vt := os.Getenv("VAULT_TOKEN")
	if vt == "" {
		vt = DefaultTestRootId
	}

	var cfg api.Config
	cfg.Address = "http://127.0.0.1:8200"
	vc, err := api.NewClient(&cfg)
	if err != nil {
		panic(err)
	}

	vc.Sys().Mount("transit", &api.MountInput{
		Type: "transit",
	})

	return vc
}
