package deploy

import (
	"context"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
)

type BackendClient struct {
}

// GetStackOutputs returns the outputs (if any) for the named stack or an error if the stack cannot be found.
func (b *BackendClient) GetStackOutputs(ctx context.Context, name string) (resource.PropertyMap, error) {
	return make(resource.PropertyMap), nil
}

// GetStackResourceOutputs returns the resource outputs for a stack, or an error if the stack
// cannot be found. Resources are retrieved from the latest stack snapshot, which may include
// ongoing updates. They are returned in a `PropertyMap` mapping resource URN to another
// `Propertymap` with members `type` (containing the Pulumi type ID for the resource) and
// `outputs` (containing the resource outputs themselves).
func (b *BackendClient) GetStackResourceOutputs(
	ctx context.Context, name string) (resource.PropertyMap, error) {
	return make(resource.PropertyMap), nil
}

