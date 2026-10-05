package errx

// CreateByCode creates an Error using the provided code, description, and message.
// This is a convenience function that directly calls New() or Wrap().
func CreateByCode(code, description, message string, cause error) *Error {
	if cause != nil {
		return Wrap(code, description, message, cause)
	}
	return New(code, description, message)
}

// FromBase creates an Error from a base error and optional message/cause.
// This is useful when you have a base error and want to create an errx.Error
// with the same category. The base is used to determine the category via a lookup function.
// Panics if base is nil (base is required for error identification).
func FromBase(base error, lookup func(error) (code, description string), message string, cause error) *Error {
	if base == nil {
		panic("errx.FromBase: base cannot be nil")
	}
	code, desc := lookup(base)
	if code == "" {
		code = CodeCLI
		desc = DescCLI
	}
	return CreateByCode(code, desc, message, cause).WithBase(base)
}

// CLI creates a CLI/argument validation error with code 70000.
// Use this for errors related to command-line argument validation,
// invalid user input, or CLI-specific issues.
// This is heavily used in internal/cli/errors.go for CLI base errors.
func CLI(message string) *Error {
	return New(CodeCLI, DescCLI, message)
}

// WrapCLI wraps a cause with a CLI/argument validation error.
// Use this when a CLI error is caused by another error that should be preserved.
func WrapCLI(message string, cause error) *Error {
	return Wrap(CodeCLI, DescCLI, message, cause)
}

// Operator creates an operator error.
// This is used in internal/operator/errors.go for operator-specific errors.
func Operator(message string) *Error {
	return New(CodeOperator, DescOperator, message)
}

// WrapOperator wraps a cause with an operator error.
// This is used in internal/operator/errors.go for operator-specific errors.
func WrapOperator(message string, cause error) *Error {
	return Wrap(CodeOperator, DescOperator, message, cause)
}
