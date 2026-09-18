package smppclient

import (
	"fmt"
	"time"
)

type PermanentError struct {
	Err error
}

// PartialSubmitError preserves the number of planned PDUs even when a later
// PDU fails. Accepted IDs are returned alongside this error by SendAll.
type PartialSubmitError struct {
	Err   error
	Total int
}

func (e PartialSubmitError) Error() string   { return e.Err.Error() }
func (e PartialSubmitError) Unwrap() error   { return e.Err }
func (e PartialSubmitError) TotalParts() int { return e.Total }

func (e PermanentError) Error() string { return e.Err.Error() }
func (e PermanentError) Unwrap() error { return e.Err }
func (e PermanentError) Permanent() bool {
	return true
}

type SubmitStatusError struct {
	Status uint32
}

func (e SubmitStatusError) Error() string {
	return fmt.Sprintf("submit_sm_resp status=0x%08x", e.Status)
}

func (e SubmitStatusError) SMPPStatus() uint32 {
	return e.Status
}

type TimeoutError struct {
	Duration time.Duration
}

func (e TimeoutError) Error() string {
	return fmt.Sprintf("smpp submit_sm_resp timeout after %s", e.Duration)
}
