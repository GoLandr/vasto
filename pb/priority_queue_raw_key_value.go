package pb

import (
	"bytes"
	"container/heap"
)

// An rawItem is something we manage in a priority queue.
type rawItem struct {
	*RawKeyValue
	chanIndex int
}

// A pqRawKeyValue implements heap.Interface and holds Items.
type pqRawKeyValue []*rawItem

func (pq pqRawKeyValue) Len() int { return len(pq) }

func (pq pqRawKeyValue) Less(i, j int) bool {
	return bytes.Compare(pq[i].Key, pq[j].Key) < 0
}

func (pq pqRawKeyValue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
}

func (pq *pqRawKeyValue) Push(x interface{}) {
	rawItem := x.(*rawItem)
	*pq = append(*pq, rawItem)
}

func (pq *pqRawKeyValue) Pop() interface{} {
	old := *pq
	n := len(old)
	rawItem := old[n-1]
	*pq = old[0 : n-1]
	return rawItem
}

// MergeSorted merges multiple channels of sorted RawKeyValue values into a bigger sorted list
// and processed by the fn function.
func MergeSorted(chans []chan *RawKeyValue, limit int64, fn func(*RawKeyValue) error) (int64, error) {

	pq := make(pqRawKeyValue, 0, len(chans))

	for i := 0; i < len(chans); i++ {
		if chans[i] == nil {
			continue
		}
		keyValue := <-chans[i]
		if keyValue != nil {
			pq = append(pq, &rawItem{
				RawKeyValue: keyValue,
				chanIndex:   i,
			})
		}
	}
	heap.Init(&pq)

	var counter int64
	for pq.Len() > 0 {
		t := heap.Pop(&pq).(*rawItem)
		if err := fn(t.RawKeyValue); err != nil {
			return counter, err
		}
		counter++
		if limit > 0 && counter >= limit {
			break
		}
		newT, hasMore := <-chans[t.chanIndex]
		if hasMore {
			heap.Push(&pq, &rawItem{
				RawKeyValue: newT,
				chanIndex:   t.chanIndex,
			})
			heap.Fix(&pq, len(pq)-1)
		}
	}

	return counter, nil
}
