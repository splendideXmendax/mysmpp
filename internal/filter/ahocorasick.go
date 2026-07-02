package filter

type pattern struct {
	text string
	rule int
}

type acNode struct {
	next map[rune]int
	fail int
	out  []pattern
}

type acMatcher struct {
	nodes []acNode
}

type acMatch struct {
	Rule  int
	Start int
	End   int
	Text  string
}

func buildAC(patterns []pattern) *acMatcher {
	m := &acMatcher{nodes: []acNode{{next: map[rune]int{}}}}
	for _, p := range patterns {
		if p.text == "" {
			continue
		}
		node := 0
		for _, r := range p.text {
			next, ok := m.nodes[node].next[r]
			if !ok {
				next = len(m.nodes)
				m.nodes = append(m.nodes, acNode{next: map[rune]int{}})
				m.nodes[node].next[r] = next
			}
			node = next
		}
		m.nodes[node].out = append(m.nodes[node].out, p)
	}
	queue := make([]int, 0)
	for _, child := range m.nodes[0].next {
		queue = append(queue, child)
	}
	for head := 0; head < len(queue); head++ {
		v := queue[head]
		for r, u := range m.nodes[v].next {
			queue = append(queue, u)
			f := m.nodes[v].fail
			for f != 0 {
				if next, ok := m.nodes[f].next[r]; ok {
					f = next
					break
				}
				f = m.nodes[f].fail
			}
			if next, ok := m.nodes[f].next[r]; ok && next != u {
				m.nodes[u].fail = next
			}
			m.nodes[u].out = append(m.nodes[u].out, m.nodes[m.nodes[u].fail].out...)
		}
	}
	return m
}

func (m *acMatcher) Find(text string) []acMatch {
	if m == nil || len(m.nodes) == 0 {
		return nil
	}
	matches := []acMatch{}
	node := 0
	byteEnds := []int{}
	for i := range text {
		byteEnds = append(byteEnds, i)
	}
	byteEnds = append(byteEnds, len(text))
	runeIndex := 0
	for _, r := range text {
		for node != 0 {
			if _, ok := m.nodes[node].next[r]; ok {
				break
			}
			node = m.nodes[node].fail
		}
		if next, ok := m.nodes[node].next[r]; ok {
			node = next
		}
		for _, p := range m.nodes[node].out {
			startRune := runeIndex - runeLen(p.text) + 1
			if startRune < 0 {
				continue
			}
			matches = append(matches, acMatch{
				Rule:  p.rule,
				Start: byteEnds[startRune],
				End:   byteEnds[runeIndex+1],
				Text:  p.text,
			})
		}
		runeIndex++
	}
	return matches
}

func runeLen(text string) int {
	n := 0
	for range text {
		n++
	}
	return n
}
