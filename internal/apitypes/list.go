package apitypes

// List is the paginated envelope used by every collection endpoint.
type List[T any] struct {
	Items  []T `json:"items"  yaml:"items"`
	Total  int `json:"total"  yaml:"total"`
	Limit  int `json:"limit"  yaml:"limit"`
	Offset int `json:"offset" yaml:"offset"`
}

// NewList builds a List with a non-nil Items slice.
func NewList[T any](items []T, total, limit, offset int) List[T] {
	if items == nil {
		items = []T{}
	}
	return List[T]{Items: items, Total: total, Limit: limit, Offset: offset}
}

// Pagination defaults and bounds shared by the server and the CLI.
const (
	DefaultLimit = 50
	MaxLimit     = 500
)

// ClampLimit normalises a caller-supplied limit.
func ClampLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultLimit
	case limit > MaxLimit:
		return MaxLimit
	default:
		return limit
	}
}
