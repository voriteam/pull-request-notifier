package slack

import (
	"fmt"
	"strings"
	"testing"
)

func TestDeployedBlocks_CapsLongLists(t *testing.T) {
	const total = 2000
	prs := make([]DeployedPR, total)
	for i := range prs {
		prs[i] = DeployedPR{
			RepoName: "backend", Number: i + 1, Title: strings.Repeat("Long title ", 30),
			URL: fmt.Sprintf("https://github.com/voriteam/backend/pull/%d", i+1), LinearKeys: []string{"VOR-1"},
		}
	}

	blocks := DeployedBlocks("production", prs)
	if len(blocks) > maxBlocks {
		t.Fatalf("%d blocks, want at most %d", len(blocks), maxBlocks)
	}
	listed := 0
	for _, b := range blocks[1 : len(blocks)-1] {
		text := b["text"].(textObj)["text"].(string)
		if len(text) > maxSectionText {
			t.Errorf("section text is %d bytes, want at most %d", len(text), maxSectionText)
		}
		listed += strings.Count(text, "• ")
	}
	more := blocks[len(blocks)-1]["elements"].([]textObj)[0]["text"].(string)
	if want := fmt.Sprintf("…and %d more", total-listed); more != want || listed == 0 {
		t.Errorf("last line %q after %d listed, want %q", more, listed, want)
	}
}

func TestDeployedBlocks_ShortListHasNoMoreLine(t *testing.T) {
	blocks := DeployedBlocks("production", []DeployedPR{
		{RepoName: "backend", Number: 1, Title: "One", URL: "u1"},
		{RepoName: "composer", Number: 2, Title: "Two", URL: "u2"},
	})
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want header and one section", len(blocks))
	}
	if got := blocks[1]["text"].(textObj)["text"]; got != "• <u1|backend#1 One>\n• <u2|composer#2 Two>" {
		t.Errorf("list %q", got)
	}
}
