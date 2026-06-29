package secrets

import (
	"context"
	"encoding/json"
	"fmt"
)

// This file is the Infisical provider (S4). Like SOPS and AWS it shells out to
// the vendor CLI (`infisical`) rather than the Go SDK — same anti-bloat reasoning
// (DECISIONS) — and inherits the user's existing auth (`infisical login` or
// INFISICAL_TOKEN), so no credential passes through devstack. The whole
// environment is exported ONCE (a batch) and each ref's key is extracted.
//
// Reference shape:
//   secret://<provider>/<SECRET_NAME>        (the secret's key in the configured env)
//   secret://<provider>/<SECRET_NAME>#<json.key>   (walk into a JSON-valued secret)
//
// Provider config: ProjectID (workspace/project id), Env (environment slug),
// Opts["path"] (secrets folder, default "/").

// InfisicalKind selects this factory.
const InfisicalKind = "infisical"

// InfisicalProvider exports an Infisical environment via the `infisical` CLI.
type InfisicalProvider struct {
	name      string
	projectID string
	env       string
	path      string // folder path; "" lets the CLI default ("/")
	runner    CmdRunner
}

// InfisicalFactory builds the provider from its config.
func InfisicalFactory(cfg ProviderConfig) (Provider, error) {
	return &InfisicalProvider{
		name:      cfg.Name,
		projectID: cfg.ProjectID,
		env:       cfg.Env,
		path:      cfg.Opts["path"],
	}, nil
}

func (p *InfisicalProvider) Name() string { return p.name }

// Resolve exports the environment once and extracts each ref's secret.
func (p *InfisicalProvider) Resolve(ctx context.Context, refs []Ref) (map[string]string, error) {
	if p.runner == nil {
		p.runner = execCmdRunner{}
	}
	if _, err := p.runner.LookPath("infisical"); err != nil {
		return nil, fmt.Errorf("infisical CLI not found on PATH — install it (https://infisical.com/docs/cli) and authenticate (`infisical login` or INFISICAL_TOKEN) for the %q provider", p.name)
	}

	args := []string{"export", "--format=json"}
	if p.projectID != "" {
		args = append(args, "--projectId", p.projectID)
	}
	if p.env != "" {
		args = append(args, "--env", p.env)
	}
	if p.path != "" {
		args = append(args, "--path", p.path)
	}
	raw, err := p.runner.Output(ctx, nil, "infisical", args...)
	if err != nil {
		return nil, fmt.Errorf("infisical export (%q): %w", p.name, err)
	}
	secrets, err := parseInfisicalExport(raw)
	if err != nil {
		return nil, fmt.Errorf("infisical %q: %w", p.name, err)
	}

	out := map[string]string{}
	for _, r := range refs {
		v, ok := secrets[r.Path]
		if !ok {
			return nil, fmt.Errorf("infisical: secret %q not found in env %q", r.Path, p.env)
		}
		if r.Key != "" {
			sub, ok := lookupJSONString(v, r.Key)
			if !ok {
				return nil, fmt.Errorf("infisical: key %q not found in secret %q (is its value JSON?)", r.Key, r.Path)
			}
			v = sub
		}
		out[r.Raw] = v
	}
	return out, nil
}

// parseInfisicalExport tolerates the two shapes `infisical export --format=json`
// has emitted across versions: a flat {KEY: VALUE} object, or a list of
// {key/secretKey, value/secretValue} records.
func parseInfisicalExport(raw []byte) (map[string]string, error) {
	var flat map[string]string
	if err := json.Unmarshal(raw, &flat); err == nil && flat != nil {
		return flat, nil
	}
	var list []struct {
		Key         string `json:"key"`
		SecretKey   string `json:"secretKey"`
		Value       string `json:"value"`
		SecretValue string `json:"secretValue"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parse export output (neither object nor list of secrets): %w", err)
	}
	out := make(map[string]string, len(list))
	for _, s := range list {
		out[firstNonEmpty(s.Key, s.SecretKey)] = firstNonEmpty(s.Value, s.SecretValue)
	}
	return out, nil
}
