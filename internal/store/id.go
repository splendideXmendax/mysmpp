package store

import "strconv"

func parseGatewaySeq(id string) (uint64, bool) {
	if len(id) < 2 {
		return 0, false
	}
	base := 10
	switch id[0] {
	case 'g':
	case 'm':
		base = 36
	default:
		return 0, false
	}
	n, err := strconv.ParseUint(id[1:], base, 64)
	return n, err == nil
}
