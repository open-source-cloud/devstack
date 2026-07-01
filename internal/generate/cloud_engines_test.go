package generate

import (
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/template"
	"github.com/open-source-cloud/devstack/templates"
)

// TestCloudEngineTemplatesLint resolves each spec-28 cloud-emulation engine from
// the embedded built-in set and validates it through compose-go (the same path
// `template lint`/`template test` drive). Each must declare a non-empty provides:
// and a defaultPort: and produce a compose-valid single-service fragment.
func TestCloudEngineTemplatesLint(t *testing.T) {
	src := template.NewFSSource(templates.FS)
	cases := []struct {
		name     string
		provides string
		port     int
		wantIn   string // a literal that must survive into the rendered compose
	}{
		{"localstack", "aws", 4566, "_localstack/health"},
		{"ministack", "aws", 4566, "_ministack/ready"},
		{"nats", "nats", 4222, "-js"},
		{"kafka", "kafka", 9092, "advertise-kafka-addr=internal://shared-kafka:9092"},
		{"rabbitmq", "amqp", 5672, "rabbitmq-diagnostics -q ping"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := template.Resolve(src, tc.name, nil)
			if err != nil {
				t.Fatalf("resolve %s: %v", tc.name, err)
			}
			if res.Provides != tc.provides {
				t.Errorf("provides = %q, want %q", res.Provides, tc.provides)
			}
			if res.DefaultPort != tc.port {
				t.Errorf("defaultPort = %d, want %d", res.DefaultPort, tc.port)
			}
			compose, err := LintResolved(tc.name, res)
			if err != nil {
				t.Fatalf("lint %s: %v", tc.name, err)
			}
			if !strings.Contains(string(compose), tc.wantIn) {
				t.Errorf("compose missing %q:\n%s", tc.wantIn, compose)
			}
		})
	}
}

// TestRabbitMQSecretIsValueless asserts RABBITMQ_DEFAULT_PASS is emitted as a
// valueless env key (no plaintext) — the §7.5 secret coupling for broker creds.
func TestRabbitMQSecretIsValueless(t *testing.T) {
	src := template.NewFSSource(templates.FS)
	res, err := template.Resolve(src, "rabbitmq", nil)
	if err != nil {
		t.Fatal(err)
	}
	compose, err := LintResolved("rabbitmq", res)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compose), "RABBITMQ_DEFAULT_PASS: null") {
		t.Errorf("RABBITMQ_DEFAULT_PASS should be a valueless (null) key:\n%s", compose)
	}
}

// cloudResolver builds a graphResolver whose workspace declares the four spec-28
// engines so the new export-attr cases can be exercised directly.
func cloudResolver() *graphResolver {
	m := &config.Model{
		Workspace: config.Workspace{
			Name: "w",
			Shared: map[string]config.SharedSvc{
				"localstack": {Template: "localstack", Params: map[string]any{"region": "eu-west-1"}},
				"awsdefault": {Template: "localstack"},
				"ministack":  {Template: "ministack", Params: map[string]any{"region": "ap-south-1"}},
				"nats":       {Template: "nats"},
				"kafka":      {Template: "kafka"},
				"rabbitmq":   {Template: "rabbitmq"},
			},
		},
		Projects: map[string]config.Project{},
	}
	return &graphResolver{
		model: m,
		sharedPort: map[string]int{
			"localstack": 4566,
			"awsdefault": 4566,
			"ministack":  4566,
			"nats":       4222,
			"kafka":      9092,
			"rabbitmq":   5672,
		},
		curProject: "api",
	}
}

// TestSharedAttr_EndpointAndRegion covers the new non-secret AWS-emulation export
// attrs: endpoint resolves to the in-network alias URL, region to the param (or
// the us-east-1 default when unset).
func TestSharedAttr_EndpointAndRegion(t *testing.T) {
	r := cloudResolver()
	if got, err := r.sharedAttr("localstack", "endpoint"); err != nil || got != "http://shared-localstack:4566" {
		t.Errorf("endpoint = %q err=%v, want http://shared-localstack:4566", got, err)
	}
	if got, err := r.sharedAttr("localstack", "region"); err != nil || got != "eu-west-1" {
		t.Errorf("region = %q err=%v, want eu-west-1 (the param)", got, err)
	}
	if got, err := r.sharedAttr("awsdefault", "region"); err != nil || got != "us-east-1" {
		t.Errorf("region default = %q err=%v, want us-east-1", got, err)
	}
	// ministack shares the generic AWS-emulation export path with localstack.
	if got, err := r.sharedAttr("ministack", "endpoint"); err != nil || got != "http://shared-ministack:4566" {
		t.Errorf("ministack endpoint = %q err=%v, want http://shared-ministack:4566", got, err)
	}
	if got, err := r.sharedAttr("ministack", "region"); err != nil || got != "ap-south-1" {
		t.Errorf("ministack region = %q err=%v, want ap-south-1 (the param)", got, err)
	}
}

// TestSharedAttr_SecondaryPorts covers the per-template export-attr→secondary-port
// lookup (monitorPort/adminPort/mgmtPort). Attrs reach sharedAttr lowercased.
func TestSharedAttr_SecondaryPorts(t *testing.T) {
	r := cloudResolver()
	cases := []struct {
		name, attr, want string
	}{
		{"nats", "monitorport", "8222"},
		{"kafka", "adminport", "9644"},
		{"rabbitmq", "mgmtport", "15672"},
	}
	for _, tc := range cases {
		got, err := r.sharedAttr(tc.name, tc.attr)
		if err != nil || got != tc.want {
			t.Errorf("%s.%s = %q err=%v, want %s", tc.name, tc.attr, got, err, tc.want)
		}
	}
	// An admin port is engine-specific: nats has no adminPort.
	if _, err := r.sharedAttr("nats", "adminport"); err == nil {
		t.Error("nats.adminPort should be unknown")
	}
}

// TestSharedAttr_SecretRejected proves an inline ${ref:...secret} attribute on a
// cloud engine is rejected at resolve time (must flow through env.import).
func TestSharedAttr_SecretRejected(t *testing.T) {
	r := cloudResolver()
	if _, err := r.Ref("workspace.shared.rabbitmq.password"); err == nil || !strings.Contains(err.Error(), "secret") {
		t.Errorf("rabbitmq.password should be rejected as a secret, got %v", err)
	}
}
