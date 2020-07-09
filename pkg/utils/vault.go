package utils

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

	vc.SetToken(vt)

	vc.Sys().Mount("transit", &api.MountInput{
		Type: "transit",
	})

	vc.Sys().Mount("kv", &api.MountInput{
		Type: "kv",
		Options: map[string]string{
			"version": "2",
		},
	})
	return vc
}
