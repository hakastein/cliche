package cliche_test

import (
	"context"
	"fmt"
	"strings"
	"testing/fstest"

	"github.com/spf13/cobra"

	"github.com/hakastein/cliche"
)

// An app scans its script commands, puts them under its cobra root and runs a call with its own API module.
func Example() {
	scripts := fstest.MapFS{
		"greet.js": &fstest.MapFile{Data: []byte(`
const app = require("app");

exports.definition = {
  short: "Greet someone",
  long: "Greet someone by name.",
  args: [{ name: "name", type: "string", usage: "who to greet" }],
  flags: [{ name: "shout", type: "bool", usage: "greet loudly" }],
};

exports.command = (name, flags) => {
  const text = "Hello, " + name + "!";
  return flags.shout ? app.upper(text) : text;
};
`)},
	}

	config := cliche.Config{Name: "demo", Sources: []cliche.Source{cliche.FS("scripts", scripts)}}
	env := cliche.Env{Modules: map[string]any{
		"app": cliche.NewObject([]string{"upper"}, []any{
			cliche.Func(func(_ context.Context, args ...any) (any, error) {
				return strings.ToUpper(args[0].(string)), nil
			}),
		}),
	}}

	root := &cobra.Command{Use: "demo", SilenceUsage: true}
	cliche.Merge(config.Scan()).AddTo(root, func(cmd *cobra.Command, command *cliche.Command, in cliche.Input) error {
		value, err := cliche.Run(cmd.Context(), command, in, env)
		if err != nil {
			return err
		}
		fmt.Println(value)
		return nil
	})

	root.SetArgs([]string{"greet", "world", "--shout"})
	if err := root.Execute(); err != nil {
		fmt.Println(err)
	}
	// Output: HELLO, WORLD!
}
