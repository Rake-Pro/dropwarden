// Package queue implements pure ordering and selection logic over drop
// campaigns. It performs no I/O: no database, no network, no browser. Callers
// feed it domain snapshots and get back deterministic decisions.
package queue

import (
	"fmt"
	"strings"

	"github.com/rake-pro/dropwarden/internal/domain"
)

// SortByPrecondition topologically sorts drops so that every drop appears
// after all drops named in its PreconditionIDs. Precondition ids that are not
// present in the input slice are treated as already satisfied (external) and
// ignored. Ties are broken by the original input order, so the result is
// deterministic for a given input. If the preconditions form a cycle, an
// error naming the drops in the cycle is returned.
func SortByPrecondition(drops []domain.Drop) ([]domain.Drop, error) {
	index := make(map[string]int, len(drops))
	for i, d := range drops {
		index[d.ID] = i
	}

	// adjacency: prereq -> dependents, restricted to ids present in the input.
	indegree := make([]int, len(drops))
	dependents := make([][]int, len(drops))
	for i, d := range drops {
		seen := make(map[string]bool, len(d.PreconditionIDs))
		for _, pre := range d.PreconditionIDs {
			if seen[pre] {
				continue
			}
			seen[pre] = true
			pi, ok := index[pre]
			if !ok || pi == i {
				continue // external/missing/self precondition; ignore
			}
			dependents[pi] = append(dependents[pi], i)
			indegree[i]++
		}
	}

	// Available holds indices with indegree 0, kept in original order so
	// ties resolve deterministically.
	available := make([]int, 0, len(drops))
	for i := range drops {
		if indegree[i] == 0 {
			available = append(available, i)
		}
	}

	result := make([]domain.Drop, 0, len(drops))
	visited := make([]bool, len(drops))
	for len(available) > 0 {
		// Pop the smallest original-order index for determinism.
		minPos := 0
		for k := 1; k < len(available); k++ {
			if available[k] < available[minPos] {
				minPos = k
			}
		}
		i := available[minPos]
		available = append(available[:minPos], available[minPos+1:]...)

		visited[i] = true
		result = append(result, drops[i])

		for _, dep := range dependents[i] {
			indegree[dep]--
			if indegree[dep] == 0 {
				available = append(available, dep)
			}
		}
	}

	if len(result) != len(drops) {
		var cycle []string
		for i, d := range drops {
			if !visited[i] {
				cycle = append(cycle, d.ID)
			}
		}
		return nil, fmt.Errorf("queue: precondition cycle among drops: %s", strings.Join(cycle, ", "))
	}

	return result, nil
}
