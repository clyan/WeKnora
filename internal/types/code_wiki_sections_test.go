package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeWikiSectionPatchesPreserveRawBytesAndAnchors(t *testing.T) {
	original := "# Title\r\n\r\nintro  \r\n\r\n## A\r\nold [E1]\r\n\r\n```md\r\n## Not a section\r\n```\r\n\r\n## B\r\n  keep exact \r\n\r\n"
	sections, err := CodeWikiSections(original)
	require.NoError(t, err)
	require.Len(t, sections, 3)
	allowed := map[string]string{sections[1].ID: sections[1].Hash}
	replacement := "## A\r\nnew [E2]\r\n\r\n```md\r\n## Not a section\r\n```\r\n\r\n"
	changed, err := ApplyCodeWikiSectionPatches(original, allowed, []CodeWikiSectionPatch{{sections[1].ID, sections[1].Hash, replacement}})
	require.NoError(t, err)
	require.Equal(t, original[:sections[1].Start]+replacement+original[sections[1].End:], changed)
	diff := CodeWikiSectionChanges(original, changed)
	require.Len(t, diff, 1)
	require.Equal(t, original[sections[1].Start:sections[1].End], diff[0].Before)
	require.Equal(t, replacement, diff[0].After)
	noop, err := ApplyCodeWikiSectionPatches(original, allowed, []CodeWikiSectionPatch{})
	require.NoError(t, err)
	require.Equal(t, original, noop)
	_, err = ApplyCodeWikiSectionPatches(original, allowed, []CodeWikiSectionPatch{{sections[2].ID, sections[2].Hash, "## B\r\nrewritten"}})
	require.ErrorContains(t, err, "whitelist")
	_, err = ApplyCodeWikiSectionPatches(original, allowed, []CodeWikiSectionPatch{{sections[1].ID, "stale hash", replacement}})
	require.Error(t, err)
	_, err = ApplyCodeWikiSectionPatches(original, allowed, []CodeWikiSectionPatch{{sections[1].ID, sections[1].Hash, "## Renamed\nnew"}})
	require.ErrorContains(t, err, "headings")
	require.Error(t, ValidateCodeWikiSectionChanges(original, changed, nil))
	_, err = CodeWikiSections("## Same\nfirst\n## Same\nsecond")
	require.ErrorContains(t, err, "duplicate")
	_, err = CodeWikiSections("## A\n```go\nnot closed")
	require.ErrorContains(t, err, "unclosed")
}

func TestCodeWikiSectionAnchorVariantsCannotBypassPatchGuard(t *testing.T) {
	original := "## A\nold [E1]\n\n## B\nkeep\n"
	sections, err := CodeWikiSections(original)
	require.NoError(t, err)
	allowed := map[string]string{sections[1].ID: sections[1].Hash}
	for _, replacement := range []string{
		"## A\nnew [E2]\n  ### Added anchor\n",
		"## A\nNew anchor\n====\n",
		"## A\nnew [E2] <a id=\"added\"></a>\n",
		"## A\nnew [E2] {#added}\n",
	} {
		_, err := ApplyCodeWikiSectionPatches(original, allowed, []CodeWikiSectionPatch{{sections[1].ID, sections[1].Hash, replacement}})
		require.Error(t, err, replacement)
	}
	sections, err = CodeWikiSections("  ## A\n````md\n```\n## fenced\n````\n## B\nend\n")
	require.NoError(t, err)
	require.Len(t, sections, 3)
	require.Equal(t, "A", sections[1].Title)
	// A setext title also owns an anchor even though it is not an editable H2.
	require.Error(t, ValidateCodeWikiSectionChanges("## A\nOld\n===\n", "## A\nNew\n===\n", map[string]string{sections[1].ID: sections[1].Hash}))
}
