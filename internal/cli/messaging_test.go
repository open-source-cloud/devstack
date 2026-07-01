package cli

import (
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/orchestrate"
)

func TestMessagingGroupsRegistered(t *testing.T) {
	root := NewRootCmd(Options{})
	for _, path := range [][]string{
		{"queue", "create"}, {"queue", "list"}, {"queue", "rm"},
		{"topic", "create"}, {"topic", "list"}, {"topic", "rm"},
		{"stream", "create"}, {"stream", "list"}, {"stream", "rm"},
	} {
		c, _, err := root.Find(path)
		if err != nil || c.RunE == nil {
			t.Fatalf("%v not registered as a real command: %v", path, err)
		}
	}
}

func TestMessagingCreateFlags(t *testing.T) {
	root := NewRootCmd(Options{})
	cases := map[string][]string{
		"queue":  {"engine", "fifo", "dlq", "max-receive", "no-prefix", "project"},
		"topic":  {"engine", "subscribe", "no-prefix", "project"},
		"stream": {"engine", "partitions", "replicas", "retention", "no-prefix", "project"},
	}
	for group, flags := range cases {
		c, _, err := root.Find([]string{group, "create"})
		if err != nil {
			t.Fatalf("%s create: %v", group, err)
		}
		for _, f := range flags {
			if c.Flags().Lookup(f) == nil {
				t.Errorf("%s create missing --%s", group, f)
			}
		}
	}
}

// loadModelWS builds a config.Model from a temp workspace with the given shared
// block, so engine-inference can be exercised without a live daemon.
func loadModelWS(t *testing.T, sharedYAML string) *config.Model {
	t.Helper()
	root := writeWS(t,
		"apiVersion: devstack/v1\nkind: Workspace\nname: demo\n"+sharedYAML+"projects:\n  - { name: web, path: web }\n",
		map[string]string{"web": "apiVersion: devstack/v1\nkind: Project\nname: web\nservices:\n  app: { template: node.vite }\n"},
	)
	m, err := config.LoadAt(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return m
}

func TestMessagingEngineInference(t *testing.T) {
	tests := []struct {
		name       string
		sharedYAML string
		allowed    []string
		order      []string
		flag       string
		wantEngine string
		wantErr    bool
	}{
		{
			name:       "queue infers nats when present (native default)",
			sharedYAML: "shared:\n  nats: { template: nats }\n  redis: { template: redis }\n",
			allowed:    queueEngines, order: queueOrder, flag: "", wantEngine: "nats",
		},
		{
			name:       "queue falls back to redis when no nats",
			sharedYAML: "shared:\n  redis: { template: redis }\n",
			allowed:    queueEngines, order: queueOrder, flag: "", wantEngine: "redis",
		},
		{
			name:       "explicit --engine sqs maps to localstack instance",
			sharedYAML: "shared:\n  localstack: { template: localstack }\n",
			allowed:    queueEngines, order: queueOrder, flag: "sqs", wantEngine: "localstack",
		},
		{
			name:       "stream infers nats over kafka",
			sharedYAML: "shared:\n  kafka: { template: kafka }\n  nats: { template: nats }\n",
			allowed:    streamEngines, order: streamOrder, flag: "", wantEngine: "nats",
		},
		{
			name:       "topic infers kafka (native default)",
			sharedYAML: "shared:\n  kafka: { template: kafka }\n",
			allowed:    topicEngines, order: topicOrder, flag: "", wantEngine: "kafka",
		},
		{
			name:       "unsatisfiable inference errors (no engine, never auto-starts)",
			sharedYAML: "shared:\n  postgres: { template: postgres }\n",
			allowed:    queueEngines, order: queueOrder, flag: "", wantErr: true,
		},
		{
			name:       "invalid --engine value errors",
			sharedYAML: "shared:\n  nats: { template: nats }\n",
			allowed:    queueEngines, order: queueOrder, flag: "pulsar", wantErr: true,
		},
		{
			name:       "explicit engine not in workspace errors (never auto-starts)",
			sharedYAML: "shared:\n  nats: { template: nats }\n",
			allowed:    queueEngines, order: queueOrder, flag: "sqs", wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := orchestrate.UpDeps{Model: loadModelWS(t, tc.sharedYAML)}
			eng, err := resolveMsgEngine(d, tc.flag, tc.allowed, tc.order, "queue")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got engine %q", eng)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveMsgEngine: %v", err)
			}
			if eng != tc.wantEngine {
				t.Errorf("engine = %q, want %q", eng, tc.wantEngine)
			}
		})
	}
}

func TestEngineForFlagAndPrefix(t *testing.T) {
	for flag, want := range map[string]string{
		"sqs": "localstack", "sns": "localstack", "nats": "nats", "kafka": "kafka", "redis": "redis",
	} {
		if got := engineForFlag(flag); got != want {
			t.Errorf("engineForFlag(%q) = %q, want %q", flag, got, want)
		}
	}
	if got := msgPrefixed("web", "jobs", false); got != "web-jobs" {
		t.Errorf("msgPrefixed = %q, want web-jobs", got)
	}
	if got := msgPrefixed("web", "external", true); got != "external" {
		t.Errorf("msgPrefixed --no-prefix = %q, want external", got)
	}
}

// TestValidateStreamFlags asserts the parse-time guard: --partitions/--replicas are
// Kafka-only (rejected for NATS) and --retention must parse (spec 29).
func TestValidateStreamFlags(t *testing.T) {
	if err := validateStreamFlags("nats", true, false, ""); err == nil {
		t.Error("--partitions with --engine nats must error")
	}
	if err := validateStreamFlags("nats", false, true, ""); err == nil {
		t.Error("--replicas with --engine nats must error")
	}
	if err := validateStreamFlags("kafka", true, true, "168h"); err != nil {
		t.Errorf("kafka with partitions/replicas/retention must be valid: %v", err)
	}
	if err := validateStreamFlags("nats", false, false, "168h"); err != nil {
		t.Errorf("nats with only --retention must be valid: %v", err)
	}
	if err := validateStreamFlags("nats", false, false, "garbage"); err == nil {
		t.Error("invalid --retention must error")
	}
}
