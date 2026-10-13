package cliche

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

// AddTo puts the commands of c under root: each script command under the group of its path, and a hidden command per
// broken path whose call is its error. A group is made for a word no command of root holds yet; called without a
// subcommand it knows, it is a *UsageError. A group made only for broken paths is hidden. run runs a script command.
func (c *Catalog) AddTo(root *cobra.Command, run func(cmd *cobra.Command, command *Command, in Input) error) {
	// A shorter path goes first, so that a command which is also a group stands before the commands under it.
	commands := slices.Clone(c.Commands)
	slices.SortStableFunc(commands, func(a, b *Command) int { return len(a.Path) - len(b.Path) })
	for _, command := range commands {
		parent := groupOf(root, command.Path[:len(command.Path)-1], false)
		parent.AddCommand(command.Cobra(func(cmd *cobra.Command, in Input) error {
			return run(cmd, command, in)
		}))
	}
	for _, broken := range c.Broken {
		if len(broken.Path) == 0 {
			continue
		}
		parent := groupOf(root, broken.Path[:len(broken.Path)-1], true)
		if slices.ContainsFunc(parent.Commands(), func(child *cobra.Command) bool {
			return child.Name() == broken.Path[len(broken.Path)-1]
		}) {
			continue
		}
		parent.AddCommand(&cobra.Command{
			Use: broken.Path[len(broken.Path)-1], Hidden: true, DisableFlagParsing: true,
			RunE: func(*cobra.Command, []string) error { return broken.Err },
		})
	}
}

func groupOf(root *cobra.Command, path []string, hidden bool) *cobra.Command {
	parent := root
	for _, word := range path {
		var found *cobra.Command
		for _, child := range parent.Commands() {
			if child.Name() == word {
				found = child
				break
			}
		}
		if found == nil {
			found = &cobra.Command{Use: word, Short: "Run the " + word + " commands", Hidden: hidden,
				Args: cobra.ArbitraryArgs, RunE: unknownSubcommand}
			parent.AddCommand(found)
		}
		if !hidden {
			found.Hidden = false
		}
		parent = found
	}
	return parent
}

func unknownSubcommand(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return &UsageError{Message: fmt.Sprintf("%s needs a subcommand", quote(cmd.CommandPath()))}
	}
	return &UsageError{Message: fmt.Sprintf("unknown command %s for %s", quote(args[0]),
		quote(strings.TrimSpace(cmd.CommandPath())))}
}
