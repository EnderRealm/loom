package knowledge

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Blocking parameters for FindDupes. On the 2026-11-07 store (1,454 truth
// candidates), DupesMinJaccard proposed ~913 pairs whose single-link components
// reached 29 members, so a component over DupesMaxCluster is re-split by raising
// the threshold DupesThresholdStep at a time until every piece fits: one judge
// call per cluster has to stay a prompt a model can partition reliably.
const (
	DupesMinJaccard    = 0.2
	DupesMaxCluster    = 8
	DupesThresholdStep = 0.05
)

// runSuffix is the `--<YYYYMMDD-HHMMSS>` stamp extract.py appends to a
// candidate's filename so a re-extraction files a sibling rather than
// overwriting. Stripped from the id as well as the filename, so a frontmatter
// id that carries the stamp keys the same as one that does not.
var runSuffix = regexp.MustCompile(`--\d{8}-\d{6}$`)

// DupeKey is the identity same-id grouping uses: the candidate's id with any
// run stamp removed.
func DupeKey(id string) string {
	return runSuffix.ReplaceAllString(id, "")
}

// DupeNode is one candidate truth id and every file filed under it. A node of
// two or more files is a same-id cluster; blocking and the judge treat the node
// as one member.
type DupeNode struct {
	Key   string
	Files []Artifact // sorted by Path
}

// Dupes is FindDupes' result: the same-id nodes (two or more files under one
// key), the blocked clusters (two to DupesMaxCluster nodes whose title and
// claim keywords overlap) that the judge partitions, and the nodes the size cap
// shed — blocked at DupesMinJaccard, but left in no cluster once their
// oversized component was re-split at a higher threshold. A same-id node can
// also be a member of a blocked cluster. Every list is sorted by key.
type Dupes struct {
	Candidates int
	SameID     []DupeNode
	Blocked    [][]DupeNode
	Shed       []DupeNode
}

// FindDupes groups the truth candidates in arts by DupeKey, then blocks the
// resulting nodes by keyword Jaccard over title + claim across every scope —
// scope directories are leaky, so a within-scope pass would miss the
// duplicates a project filed under another's directory.
func FindDupes(arts []Artifact) Dupes {
	byKey := map[string][]Artifact{}
	count := 0
	for _, a := range arts {
		if a.Status != "candidate" || a.Type != "truth" {
			continue
		}
		count++
		k := DupeKey(a.ID)
		byKey[k] = append(byKey[k], a)
	}
	nodes := make([]DupeNode, 0, len(byKey))
	for k, files := range byKey {
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		nodes = append(nodes, DupeNode{Key: k, Files: files})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Key < nodes[j].Key })

	out := Dupes{Candidates: count}
	sets := make([][]map[string]bool, len(nodes))
	all := make([]int, len(nodes))
	for i, n := range nodes {
		if len(n.Files) > 1 {
			out.SameID = append(out.SameID, n)
		}
		for _, f := range n.Files {
			sets[i] = append(sets[i], keywordSet(f.Title+"\n"+f.Claim))
		}
		all[i] = i
	}

	var edges []dupeEdge
	for i := range nodes {
		for j := i + 1; j < len(nodes); j++ {
			if s := nodeScore(sets[i], sets[j]); s >= DupesMinJaccard {
				edges = append(edges, dupeEdge{a: i, b: j, score: s})
			}
		}
	}

	clusters, shed := splitComponents(all, edges, DupesMinJaccard, false)
	for _, c := range clusters {
		cluster := make([]DupeNode, len(c))
		for i, idx := range c {
			cluster[i] = nodes[idx]
		}
		out.Blocked = append(out.Blocked, cluster)
	}
	for _, idx := range shed {
		out.Shed = append(out.Shed, nodes[idx])
	}
	return out
}

type dupeEdge struct {
	a, b  int
	score float64
}

// nodeScore is the highest Jaccard between any file of one node and any file of
// the other: re-extractions of one id word its claim differently, and any one
// wording overlapping is enough to propose the pair.
func nodeScore(a, b []map[string]bool) float64 {
	best := 0.0
	for _, x := range a {
		for _, y := range b {
			if s := jaccard(x, y); s > best {
				best = s
			}
		}
	}
	return best
}

func jaccard(a, b map[string]bool) float64 {
	inter := 0
	for w := range a {
		if b[w] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// splitComponents returns the connected components of members over the edges
// scoring at least threshold, re-splitting any component over DupesMaxCluster
// at a higher threshold. A singleton is unmatched at the top level and shed
// within a re-split, where it was blocked before the cap cut it loose. A
// component still over the cap once no higher threshold is left (every edge in
// it at 1) is chunked instead, so the strongest duplicates are never shed.
// Each cluster is sorted ascending, clusters by their first member, shed
// ascending.
func splitComponents(members []int, edges []dupeEdge, threshold float64, resplit bool) (clusters [][]int, shed []int) {
	in := map[int]bool{}
	parent := map[int]int{}
	for _, m := range members {
		in[m] = true
		parent[m] = m
	}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	for _, e := range edges {
		if e.score < threshold || !in[e.a] || !in[e.b] {
			continue
		}
		if ra, rb := find(e.a), find(e.b); ra != rb {
			parent[ra] = rb
		}
	}
	groups := map[int][]int{}
	for _, m := range members {
		r := find(m)
		groups[r] = append(groups[r], m)
	}

	for _, g := range groups {
		sort.Ints(g)
		next := threshold + DupesThresholdStep
		switch {
		case len(g) < 2:
			if resplit {
				shed = append(shed, g[0])
			}
		case len(g) <= DupesMaxCluster:
			clusters = append(clusters, g)
		case next > 1:
			clusters = append(clusters, chunk(g)...)
		default:
			c, s := splitComponents(g, edges, next, true)
			clusters = append(clusters, c...)
			shed = append(shed, s...)
		}
	}
	sort.Slice(clusters, func(i, j int) bool { return clusters[i][0] < clusters[j][0] })
	sort.Ints(shed)
	return clusters, shed
}

// chunk cuts a sorted group over DupesMaxCluster into the fewest pieces that
// fit, balanced so that none is a singleton.
func chunk(g []int) [][]int {
	n := (len(g) + DupesMaxCluster - 1) / DupesMaxCluster
	var out [][]int
	for i := 0; i < n; i++ {
		out = append(out, g[i*len(g)/n:(i+1)*len(g)/n])
	}
	return out
}

// JudgeGroup is one same-truth group of a judged cluster: the node keys it
// holds and the judge's reason. A group of one is a member the judge called
// distinct from the rest.
type JudgeGroup struct {
	IDs    []string `json:"ids"`
	Reason string   `json:"reason"`
}

// Same reports whether the judge called the group's members one truth.
func (g JudgeGroup) Same() bool { return len(g.IDs) > 1 }

// Judge partitions one blocked cluster into same-truth groups. Called once per
// cluster, never per pair: the partition has to be consistent across the
// cluster, which pairwise calls cannot guarantee.
type Judge interface {
	Partition(cluster []DupeNode) ([]JudgeGroup, error)
}

// JudgeCluster asks j to partition cluster and holds the reply to being a
// partition: every member exactly once, nothing the cluster does not hold, no
// empty group. The groups come back ordered by their first member's position
// in the cluster, members likewise, so the report does not depend on the
// order the judge chose.
func JudgeCluster(j Judge, cluster []DupeNode) ([]JudgeGroup, error) {
	groups, err := j.Partition(cluster)
	if err != nil {
		return nil, err
	}
	pos := map[string]int{}
	for i, n := range cluster {
		pos[n.Key] = i
	}
	seen := map[string]bool{}
	for gi := range groups {
		g := &groups[gi]
		if len(g.IDs) == 0 {
			return nil, fmt.Errorf("judge reply: group %d is empty", gi)
		}
		for _, id := range g.IDs {
			if _, ok := pos[id]; !ok {
				return nil, fmt.Errorf("judge reply: %q is not a member of the cluster", id)
			}
			if seen[id] {
				return nil, fmt.Errorf("judge reply: %q appears in more than one group", id)
			}
			seen[id] = true
		}
		sort.Slice(g.IDs, func(a, b int) bool { return pos[g.IDs[a]] < pos[g.IDs[b]] })
	}
	var missing []string
	for _, n := range cluster {
		if !seen[n.Key] {
			missing = append(missing, n.Key)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("judge reply: no group holds %s", strings.Join(missing, ", "))
	}
	sort.Slice(groups, func(a, b int) bool { return pos[groups[a].IDs[0]] < pos[groups[b].IDs[0]] })
	return groups, nil
}
