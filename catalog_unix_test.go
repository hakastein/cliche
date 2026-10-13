//go:build unix

package cliche_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hakastein/cliche"
)

func TestScanFollowsSymlinks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	elsewhere := t.TempDir()
	scriptsAt(t, elsewhere, map[string]string{"map.js": command})
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(dir, "docs")))

	assert.Equal(t, []summary{{commands: []string{"docs map"}, broken: []string{}}},
		summarize(cliche.Config{Sources: []cliche.Source{cliche.Dir(dir)}}.Scan()))
}

func TestUnreadableRootIsBrokenAtNoPath(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root reads a directory of no permissions")
	}
	base := t.TempDir()
	locked := filepath.Join(base, "locked")
	require.NoError(t, os.Mkdir(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	home := filepath.Join(base, "home")
	require.NoError(t, os.Mkdir(home, 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(home, ".app"), 0o000))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(home, ".app"), 0o755) })
	tests := []struct {
		name    string
		sources []cliche.Source
	}{
		{name: "a directory that cannot be listed", sources: []cliche.Source{cliche.Dir(locked)}},
		{name: "a default source that cannot be reached", sources: cliche.DefaultSources("app", "", home)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			catalog := cliche.Merge(cliche.Config{Sources: tc.sources}.Scan())

			require.Len(t, catalog.Broken, 1)
			assert.Nil(t, catalog.Broken[0].Path)
			assert.NotEmpty(t, catalog.Broken[0].Err.File)
		})
	}
}
