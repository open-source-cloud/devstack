package config_test

import (
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
)

func TestValidateWorkspaceBytes(t *testing.T) {
	valid := "apiVersion: devstack/v1\nkind: Workspace\nname: demo\n"
	if err := config.ValidateWorkspaceBytes([]byte(valid)); err != nil {
		t.Fatalf("valid workspace rejected: %v", err)
	}

	bad := []struct{ name, yaml string }{
		{"uppercase name", "apiVersion: devstack/v1\nkind: Workspace\nname: Bad\n"},
		{"underscore-leading name", "apiVersion: devstack/v1\nkind: Workspace\nname: _x\n"},
		{"missing name", "apiVersion: devstack/v1\nkind: Workspace\n"},
		{"wrong apiVersion", "apiVersion: devstack/v2\nkind: Workspace\nname: demo\n"},
		{"wrong kind", "apiVersion: devstack/v1\nkind: Project\nname: demo\n"},
		{"bad alias", "apiVersion: devstack/v1\nkind: Workspace\nname: demo\naliases:\n- BAD\n"},
		{"bad project name", "apiVersion: devstack/v1\nkind: Workspace\nname: demo\nprojects:\n- name: Bad\n  path: x\n"},
		{"project missing path", "apiVersion: devstack/v1\nkind: Workspace\nname: demo\nprojects:\n- name: ok\n"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if err := config.ValidateWorkspaceBytes([]byte(tc.yaml)); err == nil {
				t.Errorf("expected validation error, got nil for:\n%s", tc.yaml)
			}
		})
	}
}
