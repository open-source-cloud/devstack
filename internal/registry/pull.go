package registry

import (
	"context"
	"encoding/json"
	"fmt"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
)

// Pulled is the outcome of a Pull: the resolved Descriptor plus the raw bundle tar
// bytes (the single layer). The caller unpacks the tar atomically into the digest
// cache.
type Pulled struct {
	Descriptor Descriptor
	// Tar is the bundle tar (the artifact's single layer), verified against the
	// layer digest by oras during fetch.
	Tar []byte
}

// Pull resolves ref, fetches its manifest + single bundle layer, and returns the
// tar bytes. Digest verification is MANDATORY and always-on: when ref carries a
// pinned digest, the resolved manifest digest MUST equal it or the pull is refused
// (nothing is unpacked). oras additionally verifies every fetched blob against its
// descriptor digest, so a corrupt/tampered layer is rejected before it reaches the
// caller (spec 19 §"Digest verification is mandatory").
func (c *Client) Pull(ctx context.Context, ref Reference) (Pulled, error) {
	target, err := c.newTarget(ctx, ref)
	if err != nil {
		return Pulled{}, err
	}

	manDesc, manBytes, err := oras.FetchBytes(ctx, target, ref.shortRef(), oras.DefaultFetchBytesOptions)
	if err != nil {
		return Pulled{}, wrapAuth(err, ref, "fetch manifest")
	}
	// Pin enforcement: a pinned ref must resolve to exactly that manifest digest.
	if ref.Digest != "" && manDesc.Digest.String() != ref.Digest {
		return Pulled{}, fmt.Errorf("digest mismatch for %s: expected %s, registry served %s — refusing (tampered or re-pushed tag)", ref.Name(), ref.Digest, manDesc.Digest.String())
	}

	var man ocispec.Manifest
	if err := json.Unmarshal(manBytes, &man); err != nil {
		return Pulled{}, fmt.Errorf("parse manifest for %s: %w", ref.Name(), err)
	}
	if man.ArtifactType != "" && man.ArtifactType != ArtifactType && man.Config.MediaType != ArtifactType {
		return Pulled{}, fmt.Errorf("%s is not a devstack template artifact (artifactType %q)", ref.Name(), man.ArtifactType)
	}
	layer, err := bundleLayer(man)
	if err != nil {
		return Pulled{}, fmt.Errorf("%s: %w", ref.Name(), err)
	}

	tarData, err := content.FetchAll(ctx, target, layer)
	if err != nil {
		return Pulled{}, wrapAuth(err, ref, "fetch bundle layer")
	}

	desc := Descriptor{
		Ref:           refWithDigest(ref, manDesc.Digest.String()).String(),
		Repository:    ref.Name(),
		Tag:           ref.Tag,
		Digest:        manDesc.Digest.String(),
		SchemaVersion: schemaVersionFromManifest(man),
		Name:          man.Annotations[annotationTemplateName],
		Size:          manDesc.Size,
	}
	return Pulled{Descriptor: desc, Tar: tarData}, nil
}

// bundleLayer returns the single template-tar layer of a bundle manifest.
func bundleLayer(man ocispec.Manifest) (ocispec.Descriptor, error) {
	for _, l := range man.Layers {
		if l.MediaType == LayerMediaType {
			return l, nil
		}
	}
	if len(man.Layers) == 1 {
		return man.Layers[0], nil
	}
	return ocispec.Descriptor{}, fmt.Errorf("manifest has no %s layer", LayerMediaType)
}

// schemaVersionFromManifest reads the bundle schemaVersion annotation (0 if absent
// or malformed — the caller re-reads it from the unpacked template.yaml too).
func schemaVersionFromManifest(man ocispec.Manifest) int {
	var v int
	_, _ = fmt.Sscanf(man.Annotations[annotationSchemaVersion], "%d", &v)
	return v
}
