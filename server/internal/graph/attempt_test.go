package graph

import (
	"errors"
	"testing"
)

func TestModelCallRetriesTwiceThenSucceeds(t *testing.T) {
	calls := 0
	text, err := attemptCall(func() (string, error) {
		calls++
		if calls < 3 {
			return "", errors.New("down")
		}
		return `{"entities":[],"relations":[]}`, nil
	})
	if err != nil || text == "" || calls != 3 {
		t.Fatalf("text %q err %v calls %d", text, err, calls)
	}
}

func TestModelCallStopsAfterThreeFailures(t *testing.T) {
	calls := 0
	_, err := attemptCall(func() (string, error) {
		calls++
		return "", errors.New("down")
	})
	if err == nil || calls != 3 {
		t.Fatalf("err %v calls %d", err, calls)
	}
}
