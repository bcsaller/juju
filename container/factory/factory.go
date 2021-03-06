// Copyright 2013 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

// This package exists solely to avoid circular imports.

package factory

import (
	"github.com/juju/errors"

	"github.com/juju/juju/container"
	"github.com/juju/juju/container/kvm"
	"github.com/juju/juju/container/lxd"
	"github.com/juju/juju/instance"
)

// NewContainerManager creates the appropriate container.Manager for the
// specified container type.
func NewContainerManager(forType instance.ContainerType, conf container.ManagerConfig, imageURLGetter container.ImageURLGetter) (container.Manager, error) {
	switch forType {
	case instance.LXD:
		return lxd.NewContainerManager(conf)
	case instance.KVM:
		return kvm.NewContainerManager(conf)
	}
	return nil, errors.Errorf("unknown container type: %q", forType)
}
