package docker

import (
	"context"
	"errors"
	"testing"
)

func TestContainerDetailsHealth(t *testing.T) {
	cases := []struct {
		health   HealthStatus
		hasCheck bool
		healthy  bool
	}{
		{"", false, false},
		{HealthNone, false, false},
		{HealthStarting, true, false},
		{HealthHealthy, true, true},
		{HealthUnhealthy, true, false},
	}
	for _, c := range cases {
		d := ContainerDetails{Health: c.health}
		if got := d.HasHealthcheck(); got != c.hasCheck {
			t.Errorf("HasHealthcheck(%q) = %v, want %v", c.health, got, c.hasCheck)
		}
		if got := d.Healthy(); got != c.healthy {
			t.Errorf("Healthy(%q) = %v, want %v", c.health, got, c.healthy)
		}
	}
}

func TestMockContainerInspect(t *testing.T) {
	ctx := context.Background()
	m := &MockClient{Details: map[string]ContainerDetails{
		"shared-postgres": {ID: "abc", Name: "shared-postgres", State: "running",
			Running: true, Health: HealthHealthy},
	}}

	d, err := m.ContainerInspect(ctx, "shared-postgres")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Running || !d.Healthy() {
		t.Errorf("inspect = %+v, want running+healthy", d)
	}

	if _, err := m.ContainerInspect(ctx, "ghost"); err == nil {
		t.Error("inspect of unseeded container should error")
	}

	sentinel := errors.New("boom")
	m.InspectErr = sentinel
	if _, err := m.ContainerInspect(ctx, "shared-postgres"); !errors.Is(err, sentinel) {
		t.Errorf("InspectErr not propagated: %v", err)
	}
}

func TestMockContainerLogsTail(t *testing.T) {
	ctx := context.Background()
	m := &MockClient{LogLines: map[string]string{
		"api": "l1\nl2\nl3\nl4\nl5\n",
	}}

	all, err := m.ContainerLogs(ctx, "api", 0)
	if err != nil {
		t.Fatal(err)
	}
	if all != "l1\nl2\nl3\nl4\nl5\n" {
		t.Errorf("tail=0 = %q, want all lines", all)
	}

	last2, err := m.ContainerLogs(ctx, "api", 2)
	if err != nil {
		t.Fatal(err)
	}
	if last2 != "l4\nl5\n" {
		t.Errorf("tail=2 = %q, want last 2 lines", last2)
	}

	// More requested than present → all returned, untouched.
	if got, _ := m.ContainerLogs(ctx, "api", 99); got != "l1\nl2\nl3\nl4\nl5\n" {
		t.Errorf("tail=99 = %q, want all lines", got)
	}

	// Unknown container → empty, no error (mirrors an empty log).
	if got, _ := m.ContainerLogs(ctx, "ghost", 5); got != "" {
		t.Errorf("unknown container logs = %q, want empty", got)
	}

	sentinel := errors.New("nope")
	m.LogsErr = sentinel
	if _, err := m.ContainerLogs(ctx, "api", 1); !errors.Is(err, sentinel) {
		t.Errorf("LogsErr not propagated: %v", err)
	}
}

func TestLastLines(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"", 5, ""},
		{"a\nb\nc\n", 0, ""},
		{"a\nb\nc\n", 2, "b\nc\n"},
		{"a\nb\nc", 2, "b\nc"}, // no trailing newline preserved
		{"a\nb\nc\n", 10, "a\nb\nc\n"},
		{"solo", 1, "solo"},
	}
	for _, c := range cases {
		if got := lastLines(c.in, c.n); got != c.want {
			t.Errorf("lastLines(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}
