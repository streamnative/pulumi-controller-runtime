package reconcile

import (
	"context"
	"fmt"
	"github.com/pkg/errors"
	"github.com/pulumi/pulumi/pkg/v3/resource/deploy"
	"github.com/pulumi/pulumi/pkg/v3/resource/stack"
	"github.com/pulumi/pulumi/pkg/v3/secrets"
	"github.com/pulumi/pulumi/sdk/v3/go/common/encoding"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strconv"
)

type SnapshotManager interface {
	GetSnapshot(ctx context.Context, obj client.Object) (SnapshotHandle, error)
	SetSnapshot(ctx context.Context, obj client.Object, snapshot *deploy.Snapshot, prior SnapshotHandle) error
}

type SnapshotHandle interface {
	GetSnapshot() *deploy.Snapshot
	GetObjectGeneration() int64
}

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

var _ SnapshotManager = &secretSnapshotter{}

type secretSnapshotHandle struct {
	snapshot *deploy.Snapshot
	objectGeneration int64
	secret *corev1.Secret
}

var _ SnapshotHandle = &secretSnapshotHandle{}

func (s secretSnapshotHandle) GetSnapshot() *deploy.Snapshot {
	return s.snapshot
}

func (s secretSnapshotHandle) GetObjectGeneration() int64 {
	return s.objectGeneration
}

// GetSnapshot gets the latest snapshot data for the given object
func (s *secretSnapshotter) GetSnapshot(ctx context.Context, obj client.Object) (SnapshotHandle, error) {
	var secret corev1.Secret
	if err := s.Get(ctx, makeSecretName(obj), &secret); err != nil {
		if apierrors.IsNotFound(err) {
			// no snapshot for this object
			return nil, nil
		}
		return nil, errors.Wrap(err, "load checkpoint")
	}

	chkpoint, err := stack.UnmarshalVersionedCheckpointToLatestCheckpoint(secret.Data["checkpoint"])
	if err != nil {
		return nil, errors.Wrap(err, "unmarshal checkpoint")
	}

	snapshot, err := stack.DeserializeCheckpoint(chkpoint)
	if err != nil {
		return nil, errors.Wrap(err, "deserialize snapshot from checkpoint")
	}

	objectGeneration, err := strconv.ParseInt(string(secret.Data["generation"]), 10, 64)
	if err != nil {
		return nil, errors.New("parse generation")
	}

	return &secretSnapshotHandle{
		snapshot:         snapshot,
		objectGeneration: objectGeneration,
		secret:           &secret,
	}, nil
}

func (s *secretSnapshotter) SetSnapshot(ctx context.Context, obj client.Object, snap *deploy.Snapshot, prior SnapshotHandle) error {

	// Prepare the secret data block
	chk, err := stack.SerializeCheckpoint(StackName(obj), snap, s.Manager, false /* showSecrets */)
	if err != nil {
		return errors.Wrap(err, "serialize snapshot")
	}
	data, err := encoding.JSON.Marshal(chk)
	if err != nil {
		return errors.Wrap(err, "marshal checkpoint")
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
	}
	if handle == nil {
		name := makeSecretName(obj)
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
			return errors.Wrapf(err, "create checkpoint")
		}
	} else {
		secret := handle.secret.DeepCopy()
		secret.Data = secretData
		err := s.Update(ctx, secret)
		if err != nil {
			// critical error: unable to persist the snapshot
			return errors.Wrapf(err, "update checkpoint")
		}
	}

	return nil
}

func makeSecretName(obj client.Object) types.NamespacedName {
	return types.NamespacedName{
		Namespace: obj.GetNamespace(),
		Name:      fmt.Sprintf("pulumi-%s", obj.GetUID()),
	}
}

func StackName(obj client.Object) tokens.QName {
	return tokens.QName(obj.GetNamespace() + tokens.QNameDelimiter + obj.GetName())
}
