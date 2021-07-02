// Copyright (c) 2020 StreamNative, Inc.. All Rights Reserved.

package plugin

import (
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/plugin"
)

func NewPooledHost(host plugin.Host) *pooledHost {
	return &pooledHost{
		Host: host,
	}
}

type pooledHost struct {
	plugin.Host
}

func (w pooledHost) Close() error {
	return nil
}

func (w pooledHost) ReallyClose() error {
	return w.Host.Close()
}

var _ plugin.Host = &pooledHost{}
