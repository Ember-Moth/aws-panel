// Package bootstrap renders narrowly scoped, versioned instance initialization.
package bootstrap

import (
	"github.com/herta-network/aws-panel/internal/instanceinit"

	"github.com/herta-network/aws-panel/internal/auth/unixpassword"
	"github.com/herta-network/aws-panel/internal/module/lightsail/contract"
)

func Supported(blueprint string) bool {
	switch blueprint {
	case "debian_11", "debian_12", "debian_13", "ubuntu_20_04", "ubuntu_22_04", "ubuntu_24_04", "ubuntu_26_04", "amazon_linux_2", "amazon_linux_2023":
		return true
	default:
		return false
	}
}

func Script(blueprint, hash string) (string, error) {
	if !Supported(blueprint) {
		return "", contract.ErrPasswordImage
	}
	if !unixpassword.ValidHash(hash) {
		return "", contract.ErrLogin
	}
	return instanceinit.RootPassword(hash)
}
