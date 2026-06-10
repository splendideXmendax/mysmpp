package smppclient

import (
	"fmt"
	"time"
)

type PermanentError struct {
	Err error
}

func (e PermanentError) Error() string { return e.Err.Error() }
func (e PermanentError) Unwrap() error { return e.Err }
func (e PermanentError) Permanent() bool {
	return true
}

type TimeoutError struct {
	Duration time.Duration
}

func (e TimeoutError) Error() string {
	return fmt.Sprintf("smpp submit_sm_resp timeout after %s", e.Duration)
}
