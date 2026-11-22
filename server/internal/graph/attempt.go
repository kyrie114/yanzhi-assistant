package graph

import "errors"

const modelAttempts = 3

var errUnparsed = errors.New("unparsed extraction")

func attemptCall(call func() (string, error)) (string, error) {
	var last error
	for range modelAttempts {
		text, err := call()
		if err == nil {
			return text, nil
		}
		last = err
	}
	return "", last
}
