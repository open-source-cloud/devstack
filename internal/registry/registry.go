package registry

import (
	"context"
	"fmt"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
)

// Media types for a devstack template artifact. The custom artifactType marks the
// artifact as opaque to non-devstack tooling (so a registry UI won't mis-render it
// as a runnable image), and each layer's mediaType is our own tar type.
const (
	ArtifactType    = "application/vnd.devstack.template.v1"
	ConfigMediaType = "application/vnd.devstack.template.config.v1+json"
	LayerMediaType  = "application/vnd.devstack.template.layer.v1.tar"
)

// createdAnnotation is pinned to a fixed value so PackManifest is reproducible
// (spec 19 determinism AC): the manifest digest is a pure function of content.
const createdAnnotation = "1970-01-01T00:00:00Z"

// Manifest annotations carrying the bundle's identity + schema gate.
const (
	annotationTemplateName  = "land.devstack.template.name"
	annotationSchemaVersion = "land.devstack.template.schemaVersion"
)

// Descriptor is the resolved identity of a pushed/pulled artifact: the human tag
// (provenance only) plus the manifest digest that is actually fetched and verified.
type Descriptor struct {
	Ref           string `json:"ref"`           // canonical oci:// form (with digest)
	Repository    string `json:"repository"`    // registry/repository
	Tag           string `json:"tag,omitempty"` // human tag (NOT used to fetch)
	Digest        string `json:"digest"`        // sha256:… manifest digest (the pin)
	SchemaVersion int    `json:"schemaVersion"` // template-bundle schemaVersion
	Name          string `json:"name"`          // template name inside the bundle
	Size          int64  `json:"size"`          // manifest size in bytes
}

// Target is the subset of oras a registry client needs: a content store that can
// be tagged and resolved. A live *remote.Repository and an in-memory store both
// satisfy it, so tests round-trip with no network.
type Target = oras.Target

// TargetResolver returns an oras Target for a repository (host/path, no tag). The
// production resolver builds an authenticated *remote.Repository; tests return a
// shared store so push/pull round-trips offline. Behind this seam the CLI never
// constructs a remote.Repository itself.
type TargetResolver func(ctx context.Context, ref Reference) (Target, error)

// Client packages, pushes, resolves and pulls template bundles. It is stateless
// apart from the TargetResolver seam.
type Client struct {
	newTarget TargetResolver
}

// New returns a Client whose TargetResolver talks to real registries, reading
// credentials from the docker/ORAS credential store (~/.docker/config.json + OS
// helpers) with a GITHUB_TOKEN fallback for GHCR. No devstack-specific token store.
func New() (*Client, error) {
	res, err := defaultTargetResolver()
	if err != nil {
		return nil, err
	}
	return &Client{newTarget: res}, nil
}

// NewWithResolver returns a Client backed by a custom TargetResolver (tests inject
// a memory/OCI store).
func NewWithResolver(r TargetResolver) *Client {
	return &Client{newTarget: r}
}

// Push packages the template directory at dir into a deterministic OCI artifact,
// pushes it to ref's repository, tags it with ref's tag, and returns the resolved
// manifest Descriptor (digest pin).
func (c *Client) Push(ctx context.Context, ref Reference, dir string) (Descriptor, error) {
	if ref.Tag == "" {
		return Descriptor{}, fmt.Errorf("push requires a tag (got %s)", ref)
	}
	tarData, name, schemaVersion, err := PackBundle(dir)
	if err != nil {
		return Descriptor{}, err
	}
	target, err := c.newTarget(ctx, ref)
	if err != nil {
		return Descriptor{}, err
	}

	layerDesc, err := oras.PushBytes(ctx, target, LayerMediaType, tarData)
	if err != nil {
		return Descriptor{}, wrapAuth(err, ref, "push layer")
	}
	layerDesc.Annotations = map[string]string{ocispec.AnnotationTitle: name}

	manifestDesc, err := oras.PackManifest(ctx, target, oras.PackManifestVersion1_1, ArtifactType, oras.PackManifestOptions{
		Layers: []ocispec.Descriptor{layerDesc},
		ManifestAnnotations: map[string]string{
			ocispec.AnnotationCreated: createdAnnotation,
			annotationTemplateName:    name,
			annotationSchemaVersion:   fmt.Sprintf("%d", schemaVersion),
		},
	})
	if err != nil {
		return Descriptor{}, wrapAuth(err, ref, "pack manifest")
	}
	if err := target.Tag(ctx, manifestDesc, ref.Tag); err != nil {
		return Descriptor{}, wrapAuth(err, ref, "tag manifest")
	}

	return Descriptor{
		Ref:           refWithDigest(ref, manifestDesc.Digest.String()).String(),
		Repository:    ref.Name(),
		Tag:           ref.Tag,
		Digest:        manifestDesc.Digest.String(),
		SchemaVersion: schemaVersion,
		Name:          name,
		Size:          manifestDesc.Size,
	}, nil
}

// ResolveDigest resolves ref (by tag or digest) to its manifest Descriptor WITHOUT
// fetching the layers — the cheap tag→digest step used by `add`/`update`.
func (c *Client) ResolveDigest(ctx context.Context, ref Reference) (Descriptor, error) {
	target, err := c.newTarget(ctx, ref)
	if err != nil {
		return Descriptor{}, err
	}
	desc, err := target.Resolve(ctx, ref.shortRef())
	if err != nil {
		return Descriptor{}, wrapAuth(err, ref, "resolve")
	}
	return Descriptor{
		Ref:        refWithDigest(ref, desc.Digest.String()).String(),
		Repository: ref.Name(),
		Tag:        ref.Tag,
		Digest:     desc.Digest.String(),
		Size:       desc.Size,
	}, nil
}
