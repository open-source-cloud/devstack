// Package ai builds the machine-readable views of devstack that AI agents
// consume, and emits the skill/instruction files a repository commits (spec 32).
//
// It never imports internal/cli: the cobra tree is passed IN as a parameter, so
// the CLI can depend on this package without a cycle. It touches no Docker, no
// ledger and no flock — like internal/ide, it is a pure derivation-and-emission
// sink, which is what lets every output be golden-tested and deterministic.
package ai

import (
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// CommandCatalog is the whole command surface as data: what an agent needs to
// answer "what can devstack do and how do I invoke it" without shelling out to
// --help 39 times.
//
// It is DERIVED from the live cobra tree rather than authored, so it cannot drift
// from the binary that produced it. That is the same reason the emitted skills
// carry navigation instead of a copy of this list.
type CommandCatalog struct {
	Binary      string    `json:"binary"`
	Version     string    `json:"version"`
	GlobalFlags []Flag    `json:"globalFlags"`
	Commands    []Command `json:"commands"`
}

// Command is one node of the tree, flattened. Path is the invocation without the
// binary name ("db user create"), which is also the key the docs drift test uses.
type Command struct {
	Path     string   `json:"path"`
	Use      string   `json:"use"`
	Short    string   `json:"short"`
	Args     string   `json:"args,omitempty"`
	Aliases  []string `json:"aliases,omitempty"`
	Group    bool     `json:"group"`    // has subcommands
	Runnable bool     `json:"runnable"` // has a Run/RunE of its own
	Flags    []Flag   `json:"flags,omitempty"`
}

// Flag is one command-local or global flag.
type Flag struct {
	Name      string `json:"name"`
	Shorthand string `json:"shorthand,omitempty"`
	Usage     string `json:"usage"`
	Type      string `json:"type"`
	Default   string `json:"default,omitempty"`
}

// Catalog walks a cobra tree into a CommandCatalog. Hidden commands and cobra's
// own `help` are omitted; everything else is reported in stable path order.
//
// Global flags are reported once at the top level rather than repeated on every
// command, because they live on the root as persistent flags and repeating them
// 100+ times would triple the payload an agent has to read.
func Catalog(root *cobra.Command, version string) CommandCatalog {
	cat := CommandCatalog{
		Binary:      root.Name(),
		Version:     version,
		GlobalFlags: collectFlags(root.PersistentFlags()),
	}
	var walk func(c *cobra.Command, prefix []string)
	walk = func(c *cobra.Command, prefix []string) {
		for _, sub := range c.Commands() {
			if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			path := append(append([]string{}, prefix...), sub.Name())
			cat.Commands = append(cat.Commands, describe(sub, path))
			walk(sub, path)
		}
	}
	walk(root, nil)
	sort.Slice(cat.Commands, func(i, j int) bool { return cat.Commands[i].Path < cat.Commands[j].Path })
	return cat
}

func describe(c *cobra.Command, path []string) Command {
	return Command{
		Path:     strings.Join(path, " "),
		Use:      c.Use,
		Short:    c.Short,
		Args:     argSpec(c.Use),
		Aliases:  append([]string{}, c.Aliases...),
		Group:    c.HasSubCommands(),
		Runnable: c.Runnable(),
		Flags:    collectFlags(c.LocalNonPersistentFlags()),
	}
}

// argSpec extracts the argument portion of a cobra Use line: "run <task...>"
// yields "<task...>". Empty when the command takes no arguments.
func argSpec(use string) string {
	_, rest, found := strings.Cut(strings.TrimSpace(use), " ")
	if !found {
		return ""
	}
	return strings.TrimSpace(rest)
}

// collectFlags renders a flag set as sorted data, skipping hidden flags.
func collectFlags(fs *pflag.FlagSet) []Flag {
	var out []Flag
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		out = append(out, Flag{
			Name:      f.Name,
			Shorthand: f.Shorthand,
			Usage:     f.Usage,
			Type:      f.Value.Type(),
			Default:   f.DefValue,
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
