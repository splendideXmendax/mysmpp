package store

import "strings"

func IsFinalReceiptState(state string) bool {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "DELIVRD", "EXPIRED", "DELETED", "UNDELIV", "REJECTD", "UNKNOWN":
		return true
	default:
		return false
	}
}

type ReceiptAggregate struct {
	State        string
	ErrorCode    int
	Final        bool
	AllDelivered bool
}

func AggregateReceipt(segments []Pending) ReceiptAggregate {
	result := ReceiptAggregate{State: "PENDING"}
	if len(segments) == 0 {
		return result
	}
	expected := 1
	seen := make(map[int]struct{}, len(segments))
	result.Final = true
	result.AllDelivered = true
	failureRank := 0
	for _, segment := range segments {
		if segment.SegmentCount > expected {
			expected = segment.SegmentCount
		}
		if segment.SegmentIndex > 0 {
			seen[segment.SegmentIndex] = struct{}{}
		}
		state := strings.ToUpper(strings.TrimSpace(segment.DLRState))
		if !IsFinalReceiptState(state) {
			result.Final = false
		}
		if !segment.DLRDelivered {
			result.AllDelivered = false
		}
		rank := receiptFailureRank(state)
		if rank > failureRank {
			failureRank = rank
			result.State = state
			result.ErrorCode = segment.DLRErrorCode
		}
	}
	if len(seen) < expected {
		result.Final = false
		result.AllDelivered = false
	}
	if !result.Final {
		result.State = "PENDING"
		result.ErrorCode = 0
		return result
	}
	if failureRank == 0 {
		result.State = "DELIVRD"
		result.ErrorCode = 0
	}
	return result
}

func receiptFailureRank(state string) int {
	switch state {
	case "REJECTD":
		return 6
	case "UNDELIV":
		return 5
	case "EXPIRED":
		return 4
	case "DELETED":
		return 3
	case "UNKNOWN":
		return 2
	default:
		return 0
	}
}
