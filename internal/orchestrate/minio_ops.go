package orchestrate

import (
	"context"
	"fmt"

	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/lock"
	"github.com/open-source-cloud/devstack/internal/resource"
)

// This file is the imperative object-storage surface behind the `s3` CLI verbs
// that go beyond generic create/drop (lifecycle / versioning / policy / cors /
// list). Each mutation mirrors the resource_ops flow (resolve instance → overlay
// → provisioner → lock → engine call → event); `ls`/`get` are lock-free reads.
// They resolve the minio provisioner from the registry as a BucketController.

// bucketController resolves the minio instance + a host-reachable Target + the
// BucketController provisioner. Shared by every s3 helper.
func bucketController(ctx context.Context, d UpDeps) (resource.BucketController, resource.Target, string, error) {
	instance, ok := ResolveInstance(d.Model, "minio")
	if !ok {
		return nil, resource.Target{}, "", fmt.Errorf("no shared \"minio\" instance in this workspace (declare one under workspace.shared and run `devstack up`)")
	}
	prov, ok := buildRegistry(d).For("minio")
	if !ok {
		return nil, resource.Target{}, "", fmt.Errorf("no minio provisioner registered")
	}
	bc, ok := prov.(resource.BucketController)
	if !ok {
		return nil, resource.Target{}, "", fmt.Errorf("minio provisioner does not support bucket controls")
	}
	target, err := engineTarget(ctx, d, "minio", instance)
	if err != nil {
		return nil, resource.Target{}, "", err
	}
	return bc, target, instance, nil
}

// ListBuckets returns the project's buckets (prefix-filtered). Lock-free read; it
// still applies the overlay so the endpoint is reachable, but records nothing.
func ListBuckets(ctx context.Context, d UpDeps, prefix string) ([]string, error) {
	bc, target, _, err := bucketController(ctx, d)
	if err != nil {
		return nil, err
	}
	return bc.ListBuckets(ctx, target, prefix)
}

// SetBucketLifecycle applies an expiry(+transition) rule and records a lifecycle
// ownership row for the project (idempotent, under the flock).
func SetBucketLifecycle(ctx context.Context, d UpDeps, project, bucket string, rule resource.LifecycleRule) error {
	bc, target, instance, err := bucketController(ctx, d)
	if err != nil {
		return err
	}
	return lock.WithLock(ctx, d.LockPath, func() error {
		if err := bc.SetLifecycle(ctx, target, bucket, rule); err != nil {
			return err
		}
		if err := d.DB.RecordProvisioned(project, "lifecycle", bucket); err != nil {
			return err
		}
		d.DB.LogEvent("provision", project, "lifecycle on "+generate.SharedAlias(instance))
		return nil
	})
}

// GetBucketLifecycle returns the bucket's lifecycle rules (lock-free read).
func GetBucketLifecycle(ctx context.Context, d UpDeps, bucket string) ([]map[string]any, error) {
	bc, target, _, err := bucketController(ctx, d)
	if err != nil {
		return nil, err
	}
	return bc.GetLifecycle(ctx, target, bucket)
}

// RemoveBucketLifecycle deletes the bucket's lifecycle config and un-tracks the
// lifecycle ownership row (under the flock).
func RemoveBucketLifecycle(ctx context.Context, d UpDeps, project, bucket string) error {
	bc, target, instance, err := bucketController(ctx, d)
	if err != nil {
		return err
	}
	return lock.WithLock(ctx, d.LockPath, func() error {
		if err := bc.RemoveLifecycle(ctx, target, bucket); err != nil {
			return err
		}
		if err := d.DB.RemoveProvisioned(project, "lifecycle", bucket); err != nil {
			return err
		}
		d.DB.LogEvent("gc.drop", project, "lifecycle "+bucket+" removed from "+generate.SharedAlias(instance))
		return nil
	})
}

// SetBucketVersioning toggles versioning (under the flock; a bucket attribute, no
// new ledger row).
func SetBucketVersioning(ctx context.Context, d UpDeps, project, bucket string, enabled bool) error {
	bc, target, instance, err := bucketController(ctx, d)
	if err != nil {
		return err
	}
	return lock.WithLock(ctx, d.LockPath, func() error {
		if err := bc.SetVersioning(ctx, target, bucket, enabled); err != nil {
			return err
		}
		state := "suspended"
		if enabled {
			state = "enabled"
		}
		d.DB.LogEvent("provision", project, "versioning "+state+" on "+bucket+"@"+generate.SharedAlias(instance))
		return nil
	})
}

// SetBucketPolicy sets a raw JSON bucket policy (under the flock).
func SetBucketPolicy(ctx context.Context, d UpDeps, project, bucket, policyJSON string) error {
	bc, target, instance, err := bucketController(ctx, d)
	if err != nil {
		return err
	}
	return lock.WithLock(ctx, d.LockPath, func() error {
		if err := bc.SetPolicy(ctx, target, bucket, policyJSON); err != nil {
			return err
		}
		d.DB.LogEvent("provision", project, "policy on "+bucket+"@"+generate.SharedAlias(instance))
		return nil
	})
}

// GetBucketPolicy returns the bucket policy JSON, "" when none (lock-free read).
func GetBucketPolicy(ctx context.Context, d UpDeps, bucket string) (string, error) {
	bc, target, _, err := bucketController(ctx, d)
	if err != nil {
		return "", err
	}
	return bc.GetPolicy(ctx, target, bucket)
}

// SetBucketCORS sets the bucket CORS rules (under the flock).
func SetBucketCORS(ctx context.Context, d UpDeps, project, bucket string, rules []s3types.CORSRule) error {
	bc, target, instance, err := bucketController(ctx, d)
	if err != nil {
		return err
	}
	return lock.WithLock(ctx, d.LockPath, func() error {
		if err := bc.SetCORS(ctx, target, bucket, rules); err != nil {
			return err
		}
		d.DB.LogEvent("provision", project, "cors on "+bucket+"@"+generate.SharedAlias(instance))
		return nil
	})
}

// GetBucketCORS returns the bucket CORS rules (lock-free read).
func GetBucketCORS(ctx context.Context, d UpDeps, bucket string) ([]s3types.CORSRule, error) {
	bc, target, _, err := bucketController(ctx, d)
	if err != nil {
		return nil, err
	}
	return bc.GetCORS(ctx, target, bucket)
}
