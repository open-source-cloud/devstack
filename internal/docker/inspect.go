package docker

import (
	"context"
	"fmt"
	"strings"

	moby "github.com/moby/moby/client"
)

// networkDriver is the driver for the tool-owned shared bridge network.
const networkDriver = "bridge"

// composeOneoffLabel marks `compose run`/`exec` one-off containers; excluding
// them keeps them from inflating ref counts (DECISIONS D5).
const composeOneoffLabel = "com.docker.compose.oneoff"

// EnsureNetwork idempotently ensures an external bridge network exists.
func (m *mobyClient) EnsureNetwork(ctx context.Context, name string, labels map[string]string) error {
	exists, err := m.NetworkExists(ctx, name)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = m.cli.NetworkCreate(ctx, name, moby.NetworkCreateOptions{
		Driver:     networkDriver,
		Attachable: true,
		Labels:     labels,
	})
	if err != nil {
		// Tolerate a concurrent creator (the flock makes this rare, but a foreign
		// process or another engine client could still win the race).
		if strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return nil
		}
		return fmt.Errorf("create network %q: %w", name, err)
	}
	return nil
}

// NetworkExists reports whether a network with the exact name exists. NetworkList
// name filtering is a substring match, so the result is checked for an exact hit.
func (m *mobyClient) NetworkExists(ctx context.Context, name string) (bool, error) {
	res, err := m.cli.NetworkList(ctx, moby.NetworkListOptions{
		Filters: moby.Filters{}.Add("name", name),
	})
	if err != nil {
		return false, fmt.Errorf("list networks: %w", err)
	}
	for _, n := range res.Items {
		if n.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// ListManaged returns containers carrying ALL the given labels, with stopped
// containers included and compose one-offs excluded.
func (m *mobyClient) ListManaged(ctx context.Context, labels map[string]string) ([]Container, error) {
	f := moby.Filters{}
	for k, v := range labels {
		f = f.Add("label", k+"="+v)
	}
	res, err := m.cli.ContainerList(ctx, moby.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	out := make([]Container, 0, len(res.Items))
	for _, s := range res.Items {
		if s.Labels[composeOneoffLabel] == "True" || s.Labels[composeOneoffLabel] == "true" {
			continue
		}
		c := Container{
			ID:     s.ID,
			Name:   primaryName(s.Names),
			Labels: s.Labels,
			State:  string(s.State),
		}
		for _, p := range s.Ports {
			if p.PublicPort == 0 {
				continue // not published to the host
			}
			ip := ""
			if p.IP.IsValid() {
				ip = p.IP.String()
			}
			c.Ports = append(c.Ports, PortBinding{
				HostIP:        ip,
				HostPort:      int(p.PublicPort),
				ContainerPort: int(p.PrivatePort),
				Protocol:      p.Type,
			})
		}
		out = append(out, c)
	}
	return out, nil
}

// primaryName returns the first container name with the leading '/' stripped.
func primaryName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}
