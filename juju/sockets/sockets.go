// Copyright 2014 Canonical Ltd.
// Copyright 2014 Cloudbase Solutions SRL
// Licensed under the AGPLv3, see LICENCE file for details.

package sockets

import (
	"github.com/juju/loggo"
	// this is only here so that godeps will produce the right deps on all platforms
	_ "gopkg.in/natefinch/npipe.v2"
)

var logger = loggo.GetLogger("juju.juju.sockets")
