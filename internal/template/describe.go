package template

import (
	"io/fs"
)

// Description is a template's metadata read WITHOUT rendering (so it never trips
// over a missing required param). Used by `devstack template list`/`lint`.
type Description struct {
	Name        string               `json:"name"`
	Description string               `json:"description,omitempty"`
	Extends     string               `json:"extends,omitempty"`
	Provides    string               `json:"provides,omitempty"`
	Exports     []string             `json:"exports,omitempty"`
	DefaultPort int                  `json:"defaultPort,omitempty"`
	Params      map[string]ParamSpec `json:"params,omitempty"`
}

// Describe reads a template's metadata from src without rendering its fragment.
func Describe(src TemplateSource, ref string) (*Description, error) {
	fsys, err := src.Resolve(ref)
	if err != nil {
		return nil, err
	}
	raw, err := fs.ReadFile(fsys, TemplateFile)
	if err != nil {
		return nil, err
	}
	meta, err := parseMeta(ref, raw)
	if err != nil {
		return nil, err
	}
	return &Description{
		Name:        ref,
		Description: meta.Description,
		Extends:     meta.Extends,
		Provides:    meta.Provides,
		Exports:     meta.Exports,
		DefaultPort: meta.DefaultPort,
		Params:      meta.Params,
	}, nil
}
