package resp

const (
	ErrBadRequest        = "Invalid request parameters"
	ErrInvalidJSON       = "Invalid JSON format"
	ErrValidation        = "Input validation failed"
	ErrDuplicateResource = "Resource already exists"
	ErrResourceNotFound  = "Resource not found"
	ErrInternalServer    = "An unexpected error occurred"
	ErrDatabase          = "Database operation failed"
	ErrUnauthorized      = "Authentication failed"
)
