// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package testutils

import (
	"crypto/tls"

	"github.com/hashicorp/horizon/pkg/utils"
)

func SelfSignedCert() ([]byte, []byte, error) {
	return utils.SelfSignedCert()
}

func TrustedTLSConfig(cert []byte) (*tls.Config, error) {
	return utils.TrustedTLSConfig(cert)
}
