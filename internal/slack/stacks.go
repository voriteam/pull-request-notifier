package slack

import "github.com/voriteam/pull-request-notifier/internal/github"

type stackRow struct {
	pr    github.TrackedPR
	depth int // 0 for the bottom of a stack
	size  int // PRs in the whole stack, set on the bottom row
}

// groupStacks groups PRs into stacks, where a PR whose base branch is another
// listed PR's head branch sits above it. Each stack is ordered bottom first,
// depth-first, and stacks keep the position of their first member in prs.
func groupStacks(prs []github.TrackedPR) [][]stackRow {
	key := func(repo, branch string) string { return repo + "\x00" + branch }
	byHead := make(map[string]int, len(prs))
	for i, pr := range prs {
		byHead[key(pr.Repo, pr.HeadRef)] = i
	}
	parent := make([]int, len(prs))
	children := make(map[int][]int)
	for i, pr := range prs {
		parent[i] = -1
		if p, ok := byHead[key(pr.Repo, pr.BaseRef)]; ok && p != i {
			parent[i] = p
			children[p] = append(children[p], i)
		}
	}

	visited := make([]bool, len(prs))
	var walk func(i, depth int, rows []stackRow) []stackRow
	walk = func(i, depth int, rows []stackRow) []stackRow {
		if visited[i] {
			return rows
		}
		visited[i] = true
		rows = append(rows, stackRow{pr: prs[i], depth: depth})
		for _, c := range children[i] {
			rows = walk(c, depth+1, rows)
		}
		return rows
	}

	var stacks [][]stackRow
	for i := range prs {
		root := i
		for steps := 0; parent[root] >= 0 && steps < len(prs); steps++ {
			root = parent[root]
		}
		if visited[root] {
			continue
		}
		rows := walk(root, 0, nil)
		rows[0].size = len(rows)
		stacks = append(stacks, rows)
	}
	return stacks
}

func flattenStacks(stacks [][]stackRow) []stackRow {
	var rows []stackRow
	for _, s := range stacks {
		rows = append(rows, s...)
	}
	return rows
}
