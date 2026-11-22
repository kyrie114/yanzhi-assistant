package graph

func attemptCall(call func() (string, error)) (string, error) {
	return call()
}
