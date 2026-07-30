package sqlutil

import (
	"strconv"
	"strings"
)

// Rebind converts question-mark placeholders to PostgreSQL-style placeholders.
func Rebind(dialect, query string) string {
	switch strings.ToLower(strings.TrimSpace(dialect)) {
	case "postgres", "postgresql", "pgx":
	default:
		return query
	}

	var builder strings.Builder
	index := 1
	for offset := 0; offset < len(query); offset++ {
		if query[offset] == '?' {
			builder.WriteByte('$')
			builder.WriteString(strconv.Itoa(index))
			index++
			continue
		}
		builder.WriteByte(query[offset])
	}
	return builder.String()
}
