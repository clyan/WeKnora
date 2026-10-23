package service

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// buildGitLabStyleArchive mirrors a GitLab repository tarball: a leading
// pax_global_header metadata entry (typeflag 'g'), the <project>-<sha>/
// root, a plain file, and a symlink.
func buildGitLabStyleArchive(t *testing.T, extra func(tw *tar.Writer)) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	pax := map[string]string{"comment": "sync"}
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader,
		PAXRecords: pax, Format: tar.FormatPAX,
	}))

	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "proj-abc123/", Typeflag: tar.TypeDir, Mode: 0o755}))
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "proj-abc123/main.go", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4}))
	_, err := tw.Write([]byte("body"))
	require.NoError(t, err)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: "proj-abc123/link", Typeflag: tar.TypeSymlink, Linkname: "main.go", Mode: 0o777,
	}))
	if extra != nil {
		extra(tw)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func TestUnpackCodeWikiArchiveSkipsTarMetadataAndLinks(t *testing.T) {
	archive := buildGitLabStyleArchive(t, nil)
	dst := t.TempDir()

	fileCount, totalBytes, err := unpackCodeWikiArchive(bytes.NewReader(archive), dst, nil, nil)
	require.NoError(t, err)
	require.EqualValues(t, 1, fileCount)
	require.EqualValues(t, 4, totalBytes)
	require.FileExists(t, filepath.Join(dst, "main.go"))
	require.NoFileExists(t, filepath.Join(dst, "link"), "symlinks never materialize")
	require.NoFileExists(t, filepath.Join(dst, "pax_global_header"))
}

func TestUnpackCodeWikiArchiveRejectsDeviceEntries(t *testing.T) {
	archive := buildGitLabStyleArchive(t, func(tw *tar.Writer) {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: "proj-abc123/dev", Typeflag: tar.TypeChar, Mode: 0o666,
			Devmajor: 1, Devminor: 3, Format: tar.FormatGNU,
		}))
	})
	dst := t.TempDir()

	_, _, err := unpackCodeWikiArchive(bytes.NewReader(archive), dst, nil, nil)
	require.ErrorContains(t, err, "only regular files are allowed")
}
