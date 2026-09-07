//go:build !windows

package client

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultHost_XDGRuntimeDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)

	host := defaultHost()

	require.True(t, strings.HasPrefix(host, "unix://"),
		"defaultHost should return a unix:// URL, got %q", host)
	path := strings.TrimPrefix(host, "unix://")

	// The composed path may exceed maxUnixSocketPathLen and fall back
	// to /tmp; only assert containment when it did not. The socket is
	// named crush-<uid>.sock.
	composed := filepath.Join(dir, filepath.Base(path))
	if len(composed) <= maxUnixSocketPathLen {
		require.True(t, strings.HasPrefix(path, dir),
			"defaultHost should use XDG_RUNTIME_DIR when the path fits, got %q", path)
	} else {
		require.True(t, strings.HasPrefix(path, "/tmp/"),
			"defaultHost should fall back to /tmp for long paths, got %q", path)
	}
}

func TestParseHostURL(t *testing.T) {
	cases := []struct {
		in       string
		scheme   string
		host     string
		wantPath string
		wantErr  bool
	}{
		{in: "unix:///tmp/crush.sock", scheme: "unix", host: "/tmp/crush.sock"},
		{in: "tcp://127.0.0.1:8080", scheme: "tcp", host: "127.0.0.1:8080"},
		{in: "tcp://127.0.0.1:8080/base", scheme: "tcp", host: "127.0.0.1:8080", wantPath: "/base"},
		{in: "npipe:////./pipe/crush.sock", scheme: "npipe", host: "//./pipe/crush.sock"},
		{in: "localhost:8080", wantErr: true},
		{in: "", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseHostURL(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.scheme, got.Scheme)
			require.Equal(t, tc.host, got.Host)
			require.Equal(t, tc.wantPath, got.Path)
		})
	}
}
