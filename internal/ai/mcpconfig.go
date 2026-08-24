package ai

// mcpServerKey is the name devstack registers itself under in .mcp.json.
const mcpServerKey = "devstack"

// mcpServer is one entry in .mcp.json's mcpServers map.
type mcpServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// buildMCPConfig renders the devstack entry for the project's .mcp.json.
//
// The whole file is NOT owned: MergeJSONKey sets only mcpServers.devstack and
// preserves every other server a teammate has configured.
//
// Command is the bare binary name, never an absolute path. An absolute path is
// correct on the machine that ran `ai install` and wrong for everyone else who
// checks the file out — which is the entire point of committing it.
func (g *Generator) buildMCPConfig() ([]Artifact, error) {
	entry, err := marshalJSON(mcpServer{
		Command: g.binary,
		Args:    []string{"ai", "mcp"},
	})
	if err != nil {
		return nil, err
	}
	a := g.artifact("mcp-config", MergeJSONKey, entry, ".mcp.json")
	a.JSONPath = []string{"mcpServers", mcpServerKey}
	return []Artifact{a}, nil
}
