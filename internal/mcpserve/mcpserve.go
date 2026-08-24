// Package mcpserve exposes devstack over the Model Context Protocol (spec 32).
//
// It is the ONLY package that imports the MCP SDK, per the project-wide rule that
// a fast-moving external dependency sits behind one internal seam. Everything it
// needs from the rest of devstack arrives through Deps as injected function
// values, so it never imports internal/cli — there is no import cycle, and the
// whole surface is testable with hand-written fakes and no subprocess.
//
// # Why the tools are the CLI
//
// Every tool handler runs the ordinary devstack command tree with --json and
// captures stdout. That is not a shortcut: it means there is no parallel API to
// keep in sync, tool semantics match the CLI exactly, and — critically — the
// lock discipline is inherited unchanged. Each call takes and releases the
// cross-process flock inside the command's own RunE, so this long-lived server
// process never holds it, and ARCHITECTURE's "stateless CLI, no daemon" model
// survives having an MCP server in front of it.
//
// # stdio hygiene
//
// An MCP stdio server must emit nothing on stdout but framed JSON-RPC. Deps.Run
// therefore captures the command's stdout into a buffer rather than letting it
// reach the real one, and the CLI forces quiet mode for this command. A test
// asserts stdout carries only protocol bytes.
package mcpserve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Deps are the capabilities the server needs from the rest of devstack. Every
// field is required unless noted; New validates them so a wiring mistake fails at
// startup rather than on the first tool call.
type Deps struct {
	// Version stamps the server identity reported to the client.
	Version string
	// Binary is the invoked command name, used in human-facing text.
	Binary string
	// Run executes a devstack command line and returns what it wrote to stdout.
	// The implementation runs a FRESH command tree per call so no state leaks
	// between requests.
	Run func(ctx context.Context, argv []string) ([]byte, error)
	// Docs lists, reads and searches the embedded documentation corpus.
	Docs DocsSource
	// Templates exposes the built-in template sources so an agent can read a
	// real, currently-shipping template before writing one.
	Templates func() fs.FS
	// Schema returns a published JSON Schema document by kind name.
	Schema func(kind string) ([]byte, error)
	// SchemaKinds lists the published schema kinds.
	SchemaKinds func() []string
}

// DocsSource is the documentation retrieval the resources and the docs tool need.
type DocsSource interface {
	List() ([]DocMeta, error)
	Read(slug string) (DocMeta, []byte, error)
}

// DocMeta is the subset of a document's metadata this package surfaces.
type DocMeta struct {
	Slug    string `json:"slug"`
	Path    string `json:"path"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Section string `json:"section"`
	Lines   int    `json:"lines"`
}

// Options control which tools are registered.
type Options struct {
	// ReadOnly registers only tools that cannot change anything.
	ReadOnly bool
	// AllowDestructive additionally registers the irreversible verbs. Off by
	// default: those tools are ABSENT rather than merely annotated, because MCP
	// has no TTY, so a mutating tool has to inject --yes and an irreversible one
	// would then run with no confirmation anywhere in the chain.
	AllowDestructive bool
}

// Built is a constructed server together with a description of what it exposes,
// so callers and tests can report the surface without asking the SDK to
// enumerate it.
type Built struct {
	Server  *mcp.Server
	Tools   []string
	Prompts []string
}

// Build constructs the server with the tools, resources and prompts selected by
// opts.
func Build(d Deps, opts Options) (Built, error) {
	if err := d.validate(); err != nil {
		return Built{}, err
	}
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "devstack",
		Title:   "devstack",
		Version: d.Version,
	}, nil)

	tools := registerTools(s, d, opts)
	registerResources(s, d)
	registerPrompts(s, d)
	return Built{Server: s, Tools: tools, Prompts: PromptNames()}, nil
}

// New builds the server and returns just the SDK handle.
func New(d Deps, opts Options) (*mcp.Server, error) {
	built, err := Build(d, opts)
	if err != nil {
		return nil, err
	}
	return built.Server, nil
}

// Serve runs the server over stdio until the context is cancelled or the client
// disconnects.
//
// A client closing the connection is a NORMAL shutdown, not a failure: an MCP
// host starts this process, talks to it, and closes stdin when the user is done.
// Reporting that as an error would make every clean session exit non-zero and
// print a scary error block, so EOF and context cancellation return nil.
func Serve(ctx context.Context, d Deps, opts Options) error {
	s, err := New(d, opts)
	if err != nil {
		return err
	}
	if err := s.Run(ctx, &mcp.StdioTransport{}); err != nil && !isCleanShutdown(err) {
		return err
	}
	return nil
}

// isCleanShutdown reports whether an error is just the peer going away.
func isCleanShutdown(err error) bool {
	switch {
	case errors.Is(err, io.EOF),
		errors.Is(err, context.Canceled),
		errors.Is(err, net.ErrClosed):
		return true
	}
	// The SDK wraps the transport error in prose ("Server is closing: EOF"), so
	// fall back to a string check for the wrapped form.
	msg := err.Error()
	return strings.HasSuffix(msg, "EOF.") || strings.HasSuffix(msg, "EOF")
}

func (d Deps) validate() error {
	switch {
	case d.Run == nil:
		return fmt.Errorf("mcpserve: Deps.Run is required")
	case d.Docs == nil:
		return fmt.Errorf("mcpserve: Deps.Docs is required")
	case d.Templates == nil:
		return fmt.Errorf("mcpserve: Deps.Templates is required")
	case d.Schema == nil || d.SchemaKinds == nil:
		return fmt.Errorf("mcpserve: Deps.Schema and Deps.SchemaKinds are required")
	}
	return nil
}

func (d Deps) binary() string {
	if d.Binary == "" {
		return "devstack"
	}
	return d.Binary
}
