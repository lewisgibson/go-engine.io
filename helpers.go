package engineio

// Pointer returns a pointer to T.
func Pointer[T any](value T) *T {
	return &value
}
