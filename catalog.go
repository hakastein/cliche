package cliche

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Config is what an app gives cliche to find its script commands.
type Config struct {
	// Name gives the directory .<Name>/scripts of the default sources.
	Name string
	// Builtin holds the scripts built into the app; it is always the first root, its files named "builtin:<path>".
	Builtin fs.FS
	// Sources nil stands for DefaultSources(Name, os.Getwd(), os.UserHomeDir()); empty stands for no sources.
	Sources []Source
	// Types are the types of the app beyond the base ones.
	Types map[string]Type
	// Keys are the keys of the app in exports.definition beside short, long, args and flags, by name.
	Keys map[string]Key
}

// Root is a source read as a catalog: its first words with their commands and broken paths.
type Root struct {
	// Name names the directory or the fs.FS of the source.
	Name    string
	Builtin bool
	// Err is the source that cannot be read at all.
	Err   *ScriptError
	Words []Word
}

// Word is everything a root holds under one first word of a path.
type Word struct {
	Word string
	// File names the file or the directory of the word.
	File     string
	Commands []*Command
	Broken   []Broken
}

// Broken is a path that is no command: a call of it ends in Err. A root that cannot be read is broken at no path.
type Broken struct {
	Path []string
	Err  *ScriptError
}

// Catalog is the commands, the broken paths and the warnings of all roots after the rule of conflicts.
type Catalog struct {
	Commands []*Command
	Broken   []Broken
	Warnings []Warning
}

// Warning is a path a later root lost to an earlier one.
type Warning struct {
	Path []string
	// File is what lost; Kept is what won.
	File    string
	Kept    string
	Message string
}

// A word cannot start with - or _, or it would read as a flag.
var wordGrammar = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

const wordRule = "lowercase letters, digits, - and _, starting with a letter or a digit"

// A symlink that leads back up would otherwise be followed forever.
const deepestPath = 8

// Scan reads every root in order: Builtin first, then Sources. It panics when Keys holds short, long, args or flags.
func (c Config) Scan() []Root {
	checkKeys(c.Keys)
	var sources []Source
	if c.Builtin != nil {
		sources = append(sources, builtinSource(c.Builtin))
	}
	external := c.Sources
	if external == nil {
		external = DefaultSources(c.Name, workingDir(), homeDir())
	}
	sources = append(sources, external...)
	roots := make([]Root, 0, len(sources))
	for _, source := range sources {
		roots = append(roots, c.scan(source))
	}
	return roots
}

func (c Config) scan(source Source) Root {
	root := Root{Name: source.name, Builtin: source.builtin}
	if source.err != nil {
		root.Err = unreadableRoot(source, source.err)
		return root
	}
	s := &scanner{source: source, types: c.Types, keys: c.Keys}
	found, err := s.dir(".", nil)
	if err != nil {
		root.Err = unreadableRoot(source, err)
		return root
	}
	for _, word := range sortedWords(found) {
		if entry := found[word]; !entry.empty() {
			root.Words = append(root.Words, Word{Word: word, File: entry.file, Commands: entry.commands,
				Broken: entry.broken})
		}
	}
	return root
}

func unreadableRoot(source Source, err error) *ScriptError {
	return &ScriptError{Message: fmt.Sprintf("the source of scripts %s cannot be read: %v", quote(source.name), err),
		File: source.name}
}

type scanner struct {
	source Source
	types  map[string]Type
	keys   map[string]Key
}

// entry is everything a source holds at one path and below it.
type entry struct {
	file     string
	commands []*Command
	broken   []Broken
}

func (e *entry) empty() bool {
	return len(e.commands) == 0 && len(e.broken) == 0
}

func brokenEntry(words []string, file, message string) *entry {
	return &entry{file: file, broken: []Broken{{Path: words, Err: &ScriptError{Message: message, File: file}}}}
}

func (s *scanner) dir(dir string, words []string) (map[string]*entry, error) {
	listed, err := fs.ReadDir(s.source.fsys, dir)
	if err != nil {
		return nil, unwrapPath(err)
	}
	files, dirs := map[string]*entry{}, map[string]*entry{}
	for _, item := range listed {
		name := item.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		at := path.Join(dir, name)
		file := s.source.display(at)
		info, err := fs.Stat(s.source.fsys, at)
		word, isScript := strings.CutSuffix(name, ".js")
		here := append(slices.Clip(words), word)
		switch {
		case err != nil:
			files[word] = brokenEntry(here, file, fmt.Sprintf("%s cannot be read: %v", quote(file), unwrapPath(err)))
		case info.IsDir() && len(here) >= deepestPath:
			dirs[name] = brokenEntry(here, file, fmt.Sprintf("directory %s lies %d directories deep, and a command "+
				"path is shorter", quote(file), len(here)))
		case info.IsDir():
			dirs[name] = s.subdir(at, here)
		case info.Mode().IsRegular() && isScript:
			files[word] = s.file(at, here)
		}
	}
	return join(files, dirs, words), nil
}

func (s *scanner) subdir(dir string, words []string) *entry {
	file := s.source.display(dir)
	below, err := s.dir(dir, words)
	if err != nil {
		return brokenEntry(words, file, fmt.Sprintf("directory %s cannot be read: %v", quote(file), err))
	}
	joined := &entry{file: file}
	for _, word := range sortedWords(below) {
		joined.commands = append(joined.commands, below[word].commands...)
		joined.broken = append(joined.broken, below[word].broken...)
	}
	return joined
}

func join(files, dirs map[string]*entry, words []string) map[string]*entry {
	joined := map[string]*entry{}
	for word, script := range files {
		joined[word] = script
	}
	for word, below := range dirs {
		script, clash := joined[word]
		if !clash {
			joined[word] = below
			continue
		}
		if script.empty() && below.empty() {
			continue
		}
		here := append(slices.Clip(words), word)
		joined[word] = brokenEntry(here, script.file, fmt.Sprintf("%s and %s both name the command %s, which is "+
			"either a script or a directory of its subcommands", quote(script.file), quote(below.file),
			quote(strings.Join(here, " "))))
	}
	for word, held := range joined {
		if held.empty() || wordGrammar.MatchString(word) {
			continue
		}
		here := append(slices.Clip(words), word)
		joined[word] = brokenEntry(here, held.file,
			fmt.Sprintf("%s names the command word %s, which is not %s", quote(held.file), quote(word), wordRule))
	}
	return joined
}

func (s *scanner) file(name string, words []string) *entry {
	file := s.source.display(name)
	source, err := fs.ReadFile(s.source.fsys, name)
	if err != nil {
		return brokenEntry(words, file, fmt.Sprintf("%s cannot be read: %v", quote(file), unwrapPath(err)))
	}
	command, failed := declaration(file, string(source), s.types, s.keys)
	switch {
	case failed != nil:
		return &entry{file: file, broken: []Broken{{Path: words, Err: failed}}}
	case command == nil:
		return &entry{file: file}
	}
	command.Path, command.source, command.name = words, s.source, name
	return &entry{file: file, commands: []*Command{command}}
}

func sortedWords(entries map[string]*entry) []string {
	words := make([]string, 0, len(entries))
	for word := range entries {
		words = append(words, word)
	}
	slices.Sort(words)
	return words
}

// Merge is the default rule of conflicts: of two equal command paths the one of the earlier root is kept and the later
// one is a warning. A root that cannot be read is a broken path.
func Merge(roots []Root) *Catalog {
	catalog := &Catalog{}
	type held struct {
		path []string
		file string
	}
	var kept []held
	clash := func(at []string, file string) bool {
		for _, earlier := range kept {
			if !slices.Equal(earlier.path, at) {
				continue
			}
			catalog.Warnings = append(catalog.Warnings, Warning{Path: at, File: file, Kept: earlier.file,
				Message: fmt.Sprintf("%s is not the command %s: %s comes first and holds %s", quote(file),
					quote(strings.Join(at, " ")), quote(earlier.file), quote(strings.Join(earlier.path, " ")))})
			return true
		}
		return false
	}
	for _, root := range roots {
		if root.Err != nil {
			catalog.Broken = append(catalog.Broken, Broken{Err: root.Err})
			continue
		}
		var added []held
		for _, word := range root.Words {
			for _, command := range word.Commands {
				if !clash(command.Path, command.File()) {
					catalog.Commands = append(catalog.Commands, command)
					added = append(added, held{path: command.Path, file: command.File()})
				}
			}
			for _, broken := range word.Broken {
				if !clash(broken.Path, broken.Err.File) {
					catalog.Broken = append(catalog.Broken, broken)
					added = append(added, held{path: broken.Path, file: broken.Err.File})
				}
			}
		}
		kept = append(kept, added...)
	}
	return catalog
}
