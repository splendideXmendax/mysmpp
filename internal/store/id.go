package store

import "strconv"

func parseGatewaySeq(id string) (uint64, bool) {
	if len(id) < 2 || id[0] != 'g' {
		return 0, false
	}
	n, err := strconv.ParseUint(id[1:], 10, 64)
	return n, err == nil
}
