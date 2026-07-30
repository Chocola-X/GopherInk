package safeid

import "strings"

// SQL returns a portable unquoted SQL identifier using ASCII letters, digits,
// and underscores. Empty identifiers fall back to "ext".
func SQL(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	var builder strings.Builder
	lastUnderscore := false
	for _, character := range value {
		valid := (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') || character == '_'
		if !valid {
			if !lastUnderscore {
				builder.WriteByte('_')
				lastUnderscore = true
			}
			continue
		}
		builder.WriteRune(character)
		lastUnderscore = false
	}
	identifier := strings.Trim(builder.String(), "_")
	if identifier == "" {
		return "ext"
	}
	if identifier[0] >= '0' && identifier[0] <= '9' {
		return "x_" + identifier
	}
	return identifier
}
