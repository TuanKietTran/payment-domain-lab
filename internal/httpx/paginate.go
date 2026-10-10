package httpx

// Paginate returns the page of items that follows cursor (the id of the last
// item of the previous page) and the cursor for the next page, or "" when the
// page is the last one.
func Paginate[T any](items []T, limit int, cursor string, id func(T) string) ([]T, string, error) {
	after, err := DecodeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	start := 0
	if after != "" {
		found := false
		for i, item := range items {
			if id(item) == after {
				start, found = i+1, true
				break
			}
		}
		if !found {
			return nil, "", InvalidCursor()
		}
	}
	end := start + limit
	if end >= len(items) {
		return append([]T{}, items[start:]...), "", nil
	}
	page := append([]T{}, items[start:end]...)
	return page, EncodeCursor(id(items[end-1])), nil
}
