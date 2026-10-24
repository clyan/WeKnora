package types

type CodeWikiSourceEntry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	IsDirectory bool   `json:"is_directory"`
	Size        int64  `json:"size"`
}

type CodeWikiSourceDirectory struct {
	SnapshotID string                `json:"snapshot_id"`
	CommitSHA  string                `json:"commit_sha"`
	Prefix     string                `json:"prefix"`
	Entries    []CodeWikiSourceEntry `json:"entries"`
	NextCursor string                `json:"next_cursor,omitempty"`
}

type CodeWikiSourcePage struct {
	PageKey         string `json:"page_key"`
	Title           string `json:"title"`
	Version         int    `json:"version"`
	MatchesSnapshot bool   `json:"matches_snapshot"`
}
type CodeWikiSourcePages struct {
	Pages      []CodeWikiSourcePage `json:"pages"`
	NextCursor string               `json:"next_cursor,omitempty"`
}
