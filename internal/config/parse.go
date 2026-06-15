package config

import (
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

// source is one on-disk YAML file retained for decoding and for resolving a
// dotted path back to a file:line:col position when reporting semantic errors.
type source struct {
	path  string
	bytes []byte
	file  *ast.File
}

// PosError is a config error anchored at a file:line:col, rendered the way the
// rest of the tool reports config problems (ARCHITECTURE §7.6). Line/Col are 0
// when a position could not be resolved (the message still stands alone).
type PosError struct {
	File string
	Line int
	Col  int
	Msg  string
}

func (e *PosError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s:%d:%d: %s", e.File, e.Line, e.Col, e.Msg)
	}
	return fmt.Sprintf("%s: %s", e.File, e.Msg)
}

// newSource reads and AST-parses path. A parse (syntax) error is returned with
// goccy's positioned formatting.
func newSource(path string) (*source, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	f, err := parser.ParseBytes(b, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %s", path, yaml.FormatError(err, false, true))
	}
	return &source{path: path, bytes: b, file: f}, nil
}

// decode unmarshals the file into v, returning a positioned error on a type or
// structural mismatch.
func (s *source) decode(v any) error {
	if err := yaml.UnmarshalWithOptions(s.bytes, v); err != nil {
		return fmt.Errorf("%s: %s", s.path, yaml.FormatError(err, false, true))
	}
	return nil
}

// posOf resolves a goccy YAML path (e.g. "$.services.api.uses[0]") to a
// line:col within this source. Returns 0,0 when the path cannot be located.
func (s *source) posOf(path string) (line, col int) {
	p, err := yaml.PathString(path)
	if err != nil {
		return 0, 0
	}
	node, err := p.FilterFile(s.file)
	if err != nil || node == nil {
		return 0, 0
	}
	tk := node.GetToken()
	if tk == nil || tk.Position == nil {
		return 0, 0
	}
	return tk.Position.Line, tk.Position.Column
}

// errAt builds a positioned semantic error at the given goccy path.
func (s *source) errAt(path, format string, args ...any) error {
	line, col := s.posOf(path)
	return &PosError{File: s.path, Line: line, Col: col, Msg: fmt.Sprintf(format, args...)}
}
