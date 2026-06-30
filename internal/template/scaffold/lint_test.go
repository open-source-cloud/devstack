package scaffold

import (
	"strings"
	"testing"
)

// TestLintMetaTemplatingHardError asserts a [[ ]] action in a meta key is a hard
// error (parseMeta reads meta keys un-rendered).
func TestLintMetaTemplatingHardError(t *testing.T) {
	manifest := []byte("schemaVersion: 1\n" +
		"description: \"uses [[ .params.x ]] in meta\"\n" +
		"service:\n  image: x\n")
	_, err := Lint(manifest, nil)
	if err == nil || !strings.Contains(err.Error(), "meta-templating") {
		t.Fatalf("want meta-templating hard error, got %v", err)
	}
}

// TestLintMetaActionInParamDefault asserts an action in a param default (a meta
// key) is also a hard error.
func TestLintMetaActionInParamDefault(t *testing.T) {
	manifest := []byte("schemaVersion: 1\nparams:\n  v:\n    default: \"[[ .params.v ]]\"\nservice:\n  image: x\n")
	if _, err := Lint(manifest, nil); err == nil {
		t.Fatal("want hard error for action in a param default")
	}
}

// TestLintAllowsActionsUnderService asserts actions under service:/volumes: are NOT
// flagged.
func TestLintAllowsActionsUnderService(t *testing.T) {
	manifest := []byte("schemaVersion: 1\nservice:\n  image: \"redis:[[ .params.v ]]\"\nvolumes:\n  d:\n    name: \"[[ .params.v ]]\"\n")
	res, err := Lint(manifest, nil)
	if err != nil {
		t.Fatalf("actions under service:/volumes: must be allowed, got %v", err)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", res.Warnings)
	}
}

// TestLintParamTypeWarn asserts a default that does not parse as its declared type
// warns (not a hard error).
func TestLintParamTypeWarn(t *testing.T) {
	manifest := []byte("schemaVersion: 1\nparams:\n  count:\n    type: int\n    default: \"abc\"\n  flag:\n    type: bool\n    default: \"nope\"\nservice:\n  image: x\n")
	res, err := Lint(manifest, nil)
	if err != nil {
		t.Fatalf("param-type is a warning, not an error: %v", err)
	}
	joined := strings.Join(res.Warnings, "\n")
	if !strings.Contains(joined, "param-type") || !strings.Contains(joined, "count") || !strings.Contains(joined, "flag") {
		t.Errorf("expected param-type warnings for count+flag, got %v", res.Warnings)
	}
}

// TestLintDelimiterCollisionWarn asserts a build/ file with a literal [[ that is not
// a valid action warns.
func TestLintDelimiterCollisionWarn(t *testing.T) {
	manifest := []byte("schemaVersion: 1\nservice:\n  image: x\n")
	build := map[string][]byte{
		"build/entrypoint.sh": []byte("#!/bin/sh\nif [[ -z \"$X\" ]]; then echo no; fi\n"),
	}
	res, err := Lint(manifest, build)
	if err != nil {
		t.Fatalf("delimiter-collision is a warning: %v", err)
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "delimiter-collision") {
		t.Errorf("expected a delimiter-collision warning, got %v", res.Warnings)
	}
}

// TestLintCleanBundle asserts a well-formed bundle produces no warnings/errors —
// including a valid [[ ]] action in a build file.
func TestLintCleanBundle(t *testing.T) {
	b, err := Build(appSpec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	res, err := Lint(b["template.yaml"], BuildFilesOf(b))
	if err != nil {
		t.Fatalf("clean bundle must not hard-error: %v", err)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("clean bundle must produce no warnings, got %v", res.Warnings)
	}
}
