/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package backend

import (
	"context"
	"fmt"
	"strconv"

	"github.com/mitchellh/copystructure"
	ot "github.com/opentracing/opentracing-go"
	otlog "github.com/opentracing/opentracing-go/log"
	"github.com/pkg/errors"
	"github.com/pulumi/pulumi/pkg/v3/resource/deploy"
	"github.com/pulumi/pulumi/pkg/v3/resource/stack"
	"github.com/pulumi/pulumi/pkg/v3/secrets"
	"github.com/pulumi/pulumi/sdk/v3/go/common/encoding"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SnapshotStorage is an interface to snapshot storage.
// Also implements BackendClient to provide information (e.g. stack outputs) about stacks from a backend.
type SnapshotStorage interface {
	deploy.BackendClient

	// GetSnapshot gets the latest snapshot for the given object.
	GetSnapshot(ctx context.Context, obj client.Object) (*deploy.Snapshot, SnapshotHandle, error)

	// SetSnapshot sets the snapshot for the given object.
	SetSnapshot(ctx context.Context, obj client.Object, snapshot *deploy.Snapshot, handle SnapshotHandle) (SnapshotHandle, error)
}

// SnapshotHandle is an opaque representation of a handle to a specific snapshot.
type SnapshotHandle interface {
	GetObjectGeneration() int64
}

// NewSecretSnapshotter provides an implementation of snapshot storage based on Kubernetes secrets.
// The snapshotter uses the given secrets manager to encrypt/decrypt state values.
func NewSecretSnapshotter(client client.Client, secretsManager secrets.Manager) *secretSnapshotter {
	return &secretSnapshotter{
		Client:  client,
		Manager: secretsManager,
	}
}

type secretSnapshotter struct {
	client.Client
	secrets.Manager
}

// region BackendClient

var _ deploy.BackendClient = &secretSnapshotter{}

func (s *secretSnapshotter) GetStackOutputs(ctx context.Context, stackName string) (props resource.PropertyMap, err error) {
	span, ctx := ot.StartSpanFromContext(ctx, "GetStackOutputs")
	defer func() {
		var f []otlog.Field
		f = append(f, logPropertyMap(props)...)
		if err != nil {
			f = append(f, otlog.Error(err))
		}
		span.LogFields(f...)
		span.Finish()
	}()

	ref, err := parseStackReference(stackName)
	if err != nil {
		return nil, err
	}
	snap, _, err := s.getSnapshot(ctx, ref)
	if err != nil {
		return nil, err
	}

	res, err := stack.GetRootStackResource(snap)
	if err != nil {
		return nil, errors.Wrap(err, "getting root stack resources")
	}
	if res == nil {
		return resource.PropertyMap{}, nil
	}
	return res.Outputs, nil
}

func (s *secretSnapshotter) GetStackResourceOutputs(ctx context.Context, stackName string) (props resource.PropertyMap, err error) {
	span, ctx := ot.StartSpanFromContext(ctx, "GetStackResourceOutputs")
	defer func() {
		var f []otlog.Field
		f = append(f, logPropertyMap(props)...)
		if err != nil {
			f = append(f, otlog.Error(err))
		}
		span.LogFields(f...)
		span.Finish()
	}()

	ref, err := parseStackReference(stackName)
	if err != nil {
		return nil, err
	}
	snap, _, err := s.getSnapshot(ctx, ref)
	if err != nil {
		return nil, err
	}

	pm := resource.PropertyMap{}
	for _, r := range snap.Resources {
		if r.Delete {
			continue
		}
		resc := resource.PropertyMap{
			resource.PropertyKey("type"):    resource.NewStringProperty(string(r.Type)),
			resource.PropertyKey("outputs"): resource.NewObjectProperty(r.Outputs)}
		pm[resource.PropertyKey(r.URN)] = resource.NewObjectProperty(resc)
	}
	return pm, nil
}

// endregion

// region SnapshotStorage

var _ SnapshotStorage = &secretSnapshotter{}

type secretSnapshotHandle struct {
	objectGeneration int64
	secret           *corev1.Secret
}

var _ SnapshotHandle = &secretSnapshotHandle{}

func (s secretSnapshotHandle) GetObjectGeneration() int64 {
	return s.objectGeneration
}

// GetSnapshot gets the latest snapshot data for the given object.
func (s *secretSnapshotter) GetSnapshot(ctx context.Context, obj client.Object) (snap *deploy.Snapshot, handle SnapshotHandle, err error) {
	span, ctx := ot.StartSpanFromContext(ctx, "GetSnapshot")
	defer func() {
		var f []otlog.Field
		f = append(f, logSnapshot(snap, handle)...)
		if err != nil {
			f = append(f, otlog.Error(err))
		}
		span.LogFields(f...)
		span.Finish()
	}()

	ref := newStackReferenceForObject(obj)
	snap, h, err := s.getSnapshot(ctx, ref)
	if err != nil {
		return nil, nil, err
	}
	if snap == nil {
		return nil, nil, nil
	}

	// Perform snapshot integrity check.
	// avoid going backwards in terms of goal state, i.e.
	// maintain the invariant that the goal generation is >= state generation
	if h.GetObjectGeneration() > obj.GetGeneration() {
		return nil, nil, ErrSnapshotGenerationMismatch
	}

	return snap, h, nil
}

func (s *secretSnapshotter) getSnapshot(ctx context.Context, ref objectReference) (*deploy.Snapshot, *secretSnapshotHandle, error) {
	var secret corev1.Secret
	if err := s.Get(ctx, makeSecretName(ref), &secret); err != nil {
		if apierrors.IsNotFound(err) {
			// no snap for this object
			return nil, nil, nil
		}
		return nil, nil, errors.Wrap(err, "load checkpoint")
	}

	chkpoint, err := stack.UnmarshalVersionedCheckpointToLatestCheckpoint(secret.Data["checkpoint"])
	if err != nil {
		return nil, nil, errors.Wrap(err, "unmarshal checkpoint")
	}

	snap, err := stack.DeserializeCheckpoint(chkpoint)
	if err != nil {
		return nil, nil, errors.Wrap(err, "deserialize snapshot from checkpoint")
	}

	objectGeneration, err := strconv.ParseInt(string(secret.Data["generation"]), 10, 64)
	if err != nil {
		return nil, nil, errors.New("parse generation")
	}

	return snap, &secretSnapshotHandle{
		objectGeneration: objectGeneration,
		secret:           &secret,
	}, nil
}

func (s *secretSnapshotter) SetSnapshot(ctx context.Context, obj client.Object, snap *deploy.Snapshot,
	prior SnapshotHandle) (handle SnapshotHandle, err error) {
	span, ctx := ot.StartSpanFromContext(ctx, "SetSnapshot")
	defer func() {
		if err != nil {
			span.LogFields(otlog.Error(err))
		}
		span.Finish()
	}()

	return s.setSnapshot(ctx, obj, snap, prior)
}

func (s *secretSnapshotter) setSnapshot(ctx context.Context, obj client.Object, snap *deploy.Snapshot,
	prior SnapshotHandle) (SnapshotHandle, error) {
	ref := newStackReferenceForObject(obj)

	// Prepare the secret data block
	chk, err := stack.SerializeCheckpoint(ref.Name(), snap, s.Manager, false /* showSecrets */)
	if err != nil {
		return nil, errors.Wrap(err, "serialize snapshot")
	}
	data, err := encoding.JSON.Marshal(chk)
	if err != nil {
		return nil, errors.Wrap(err, "marshal checkpoint")
	}
	objectGeneration := obj.GetGeneration()
	secretData := map[string][]byte{
		"checkpoint": data,
		"generation": []byte(strconv.FormatInt(objectGeneration, 10)),
	}

	// Create or update the secret
	var handle *secretSnapshotHandle
	if prior != nil {
		handle = prior.(*secretSnapshotHandle)
	} else {
		handle = &secretSnapshotHandle{}
	}

	handle.objectGeneration = objectGeneration

	if handle.secret == nil {
		name := makeSecretName(ref)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name.Name,
				Namespace: name.Namespace,
				OwnerReferences: []metav1.OwnerReference{
					*metav1.NewControllerRef(obj, obj.GetObjectKind().GroupVersionKind()),
				},
			},
			Data: secretData,
		}
		err := s.Create(ctx, secret)
		if err != nil {
			// critical error: unable to persist the snapshot
			return nil, errors.Wrapf(err, "create checkpoint")
		}
		handle.secret = secret
	} else {
		secret := handle.secret.DeepCopy()
		secret.Data = secretData
		err := s.Update(ctx, secret)
		if err != nil {
			// critical error: unable to persist the snapshot
			return nil, errors.Wrapf(err, "update checkpoint")
		}
		handle.secret = secret
	}

	return handle, nil
}

// endregion

func makeSecretName(ref objectReference) types.NamespacedName {
	return types.NamespacedName{
		Namespace: ref.Namespace,
		Name:      fmt.Sprintf("pulumi-%s", ref.UID),
	}
}

func logSnapshot(snap *deploy.Snapshot, handle SnapshotHandle) []otlog.Field {
	var f []otlog.Field
	if snap != nil {
		f = append(f, otlog.Int("pulumi.pending_operations", len(snap.PendingOperations)))
	}
	if handle != nil {
		h := handle.(*secretSnapshotHandle)
		f = append(f, otlog.Int64("pulumi.object_generation", h.objectGeneration))
		f = append(f, otlog.String("pulumi.secret_resource_version", h.secret.ResourceVersion))
	}
	return f
}

func logPropertyMap(props resource.PropertyMap) []otlog.Field {
	// TODO log the stack outputs (except for secrets)
	return nil
}

// CloneSnapshot makes a deep copy of the given snapshot and returns a pointer to the clone.
func CloneSnapshot(snap *deploy.Snapshot) *deploy.Snapshot {
	copiedSnap := copystructure.Must(copystructure.Copy(*snap)).(deploy.Snapshot)
	return &copiedSnap
}
