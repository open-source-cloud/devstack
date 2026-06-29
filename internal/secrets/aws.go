package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// This file is the AWS provider (S3): Secrets Manager (`aws-sm`) and SSM
// Parameter Store (`aws-ssm`). It shells out to the `aws` CLI — NOT the
// aws-sdk-go, which would pull a large cloud SDK tree into the CGO_ENABLED=0
// static binary (the same anti-bloat reasoning that keeps SOPS on its binary —
// DECISIONS). Shelling also inherits the user's AWS config / SSO / IAM-role
// credentials exactly as git inherits SSH, so no AWS credential ever passes
// through devstack.
//
// Reference shapes:
//   secret://<provider>/<secret-id>#<json.key>   (aws-sm: SecretString, optional JSON sub-key)
//   secret://<provider>/<parameter-name>         (aws-ssm: parameter value; optional #json.key)

// AWS provider kinds.
const (
	AWSSecretsManagerKind = "aws-sm"
	AWSSSMKind            = "aws-ssm"
)

// AWSProvider resolves secrets via the `aws` CLI. mode selects the service.
type AWSProvider struct {
	name   string
	mode   string // AWSSecretsManagerKind | AWSSSMKind
	region string // --region; "" inherits the CLI's resolved default
	runner CmdRunner
}

// AWSFactory builds an AWSProvider; the kind (aws-sm/aws-ssm) selects the mode.
// Region comes from cfg.Region or Opts["region"]; empty lets the CLI resolve it
// (AWS_REGION / profile / IMDS).
func AWSFactory(cfg ProviderConfig) (Provider, error) {
	if cfg.Kind != AWSSecretsManagerKind && cfg.Kind != AWSSSMKind {
		return nil, fmt.Errorf("aws provider %q: unsupported kind %q", cfg.Name, cfg.Kind)
	}
	return &AWSProvider{
		name:   cfg.Name,
		mode:   cfg.Kind,
		region: firstNonEmpty(cfg.Region, cfg.Opts["region"]),
	}, nil
}

func (p *AWSProvider) Name() string { return p.name }

// Resolve dispatches to the Secrets Manager or SSM batch resolver.
func (p *AWSProvider) Resolve(ctx context.Context, refs []Ref) (map[string]string, error) {
	if p.runner == nil {
		p.runner = execCmdRunner{}
	}
	if _, err := p.runner.LookPath("aws"); err != nil {
		return nil, fmt.Errorf("aws CLI not found on PATH — install it (https://aws.amazon.com/cli/) and authenticate for the %q provider", p.name)
	}
	switch p.mode {
	case AWSSecretsManagerKind:
		return p.resolveSM(ctx, refs)
	default:
		return p.resolveSSM(ctx, refs)
	}
}

// resolveSM fetches each distinct secret id once (a secret may back several refs
// via different #keys) and extracts each ref's value. A keyless ref takes the raw
// SecretString; a #key ref parses it as JSON and walks the dot-path.
func (p *AWSProvider) resolveSM(ctx context.Context, refs []Ref) (map[string]string, error) {
	byID := map[string][]Ref{}
	for _, r := range refs {
		byID[r.Path] = append(byID[r.Path], r)
	}
	out := map[string]string{}
	for _, id := range sortedKeys(byID) {
		args := []string{"secretsmanager", "get-secret-value", "--secret-id", id, "--query", "SecretString", "--output", "text"}
		raw, err := p.runner.Output(ctx, nil, "aws", p.withRegion(args)...)
		if err != nil {
			return nil, fmt.Errorf("aws-sm get-secret-value %q: %w", id, err)
		}
		secret := strings.TrimRight(string(raw), "\n")
		for _, r := range byID[id] {
			if r.Key == "" {
				out[r.Raw] = secret
				continue
			}
			v, ok := lookupJSONString(secret, r.Key)
			if !ok {
				return nil, fmt.Errorf("aws-sm: key %q not found in secret %q (is its value JSON?)", r.Key, id)
			}
			out[r.Raw] = v
		}
	}
	return out, nil
}

// ssmGetParameters mirrors the `aws ssm get-parameters` JSON envelope.
type ssmGetParameters struct {
	Parameters []struct {
		Name  string `json:"Name"`
		Value string `json:"Value"`
	} `json:"Parameters"`
	InvalidParameters []string `json:"InvalidParameters"`
}

// resolveSSM fetches every referenced parameter in ONE batched GetParameters call
// (with decryption). A ref may carry a #key to walk into a JSON-valued parameter.
func (p *AWSProvider) resolveSSM(ctx context.Context, refs []Ref) (map[string]string, error) {
	names := sortedKeys(groupByPath(refs))
	// --names is variadic and must come LAST, so apply --region to the base first.
	args := p.withRegion([]string{"ssm", "get-parameters", "--with-decryption", "--output", "json"})
	args = append(append(args, "--names"), names...)
	raw, err := p.runner.Output(ctx, nil, "aws", args...)
	if err != nil {
		return nil, fmt.Errorf("aws-ssm get-parameters: %w", err)
	}
	var resp ssmGetParameters
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("aws-ssm: parse get-parameters output: %w", err)
	}
	if len(resp.InvalidParameters) > 0 {
		return nil, fmt.Errorf("aws-ssm: parameter(s) not found: %s", strings.Join(resp.InvalidParameters, ", "))
	}
	valueByName := make(map[string]string, len(resp.Parameters))
	for _, par := range resp.Parameters {
		valueByName[par.Name] = par.Value
	}
	out := map[string]string{}
	for _, r := range refs {
		v, ok := valueByName[r.Path]
		if !ok {
			return nil, fmt.Errorf("aws-ssm: parameter %q missing from response", r.Path)
		}
		if r.Key != "" {
			sub, ok := lookupJSONString(v, r.Key)
			if !ok {
				return nil, fmt.Errorf("aws-ssm: key %q not found in parameter %q (is its value JSON?)", r.Key, r.Path)
			}
			v = sub
		}
		out[r.Raw] = v
	}
	return out, nil
}

// withRegion appends --region when configured.
func (p *AWSProvider) withRegion(args []string) []string {
	if p.region == "" {
		return args
	}
	return append(args, "--region", p.region)
}

// groupByPath buckets refs by their backend path/identifier.
func groupByPath(refs []Ref) map[string][]Ref {
	out := map[string][]Ref{}
	for _, r := range refs {
		out[r.Path] = append(out[r.Path], r)
	}
	return out
}

// lookupJSONString parses s as a JSON object and walks key's dot-path to a leaf.
func lookupJSONString(s, key string) (string, bool) {
	var data map[string]any
	if err := json.Unmarshal([]byte(s), &data); err != nil {
		return "", false
	}
	return lookupPath(data, key)
}
