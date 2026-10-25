package invocation

// Preserve storage causes for bounded retry decisions without returning their
// potentially sensitive diagnostic text through the evidence domain API.
type videoStorageError struct{ cause error }

func (e *videoStorageError) Error() string        { return ErrVideoStorage.Error() }
func (e *videoStorageError) Unwrap() error        { return e.cause }
func (e *videoStorageError) Is(target error) bool { return target == ErrVideoStorage }

func videoStorageFailure(cause error) error {
	if cause == nil {
		return nil
	}
	return &videoStorageError{cause: cause}
}
