package cliche

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Source is a directory or an fs.FS of scripts: the path of a file in it is the path of a command.
type Source struct {
	name    string
	dir     string
	fsys    fs.FS
	builtin bool
	// err is the failure to read an existing path the source stands for.
	err error
}

// Dir is a source of scripts in a directory; its files are named by their absolute paths.
func Dir(dir string) Source {
	if absolute, err := filepath.Abs(dir); err == nil {
		dir = absolute
	}
	return Source{name: dir, dir: dir, fsys: os.DirFS(dir)}
}

// FS is a source of scripts in fsys; its files are named "<name>:<path>".
func FS(name string, fsys fs.FS) Source {
	return Source{name: name, fsys: fsys}
}

func builtinSource(fsys fs.FS) Source {
	return Source{name: "builtin", fsys: fsys, builtin: true}
}

// display names a file of the source in faults and in Place.
func (s Source) display(name string) string {
	if s.dir != "" {
		return filepath.Join(s.dir, filepath.FromSlash(name))
	}
	return s.name + ":" + name
}

// DefaultSources are the nearest .<app>/scripts at or above workingDir and <home>/.<app>/scripts, in that order. The
// nearest one that is the user's own is no project source. An empty workingDir or home gives no source of its own;
// an existing path that cannot be read is a source that fails to scan.
func DefaultSources(app, workingDir, home string) []Source {
	scripts := filepath.Join("."+app, "scripts")
	var user string
	if home != "" {
		user = filepath.Join(home, scripts)
		if absolute, err := filepath.Abs(user); err == nil {
			user = absolute
		}
	}
	var sources []Source
	if workingDir != "" {
		if absolute, err := filepath.Abs(workingDir); err == nil {
			workingDir = absolute
		}
		for dir := workingDir; ; dir = filepath.Dir(dir) {
			candidate := filepath.Join(dir, scripts)
			if candidate == user {
				break
			}
			if found, isSource := probe(candidate); isSource {
				sources = append(sources, found)
				break
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	if user != "" {
		if found, isSource := probe(user); isSource {
			sources = append(sources, found)
		}
	}
	return sources
}

func probe(dir string) (Source, bool) {
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Source{}, false
	case err != nil:
		source := Dir(dir)
		source.err = unwrapPath(err)
		return source, true
	}
	return Dir(dir), info.IsDir()
}

func unwrapPath(err error) error {
	var failed *fs.PathError
	if errors.As(err, &failed) {
		return failed.Err
	}
	return err
}

func workingDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return dir
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}
