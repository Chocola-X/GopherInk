package i18n

import "strings"

const (
	DefaultLanguage  = "en-US"
	FallbackLanguage = "en-US"
)

var languageNames = map[string]string{
	"en-US": "English",
	"es-ES": "Español",
	"ja-JP": "日本語",
	"zh-CN": "简体中文",
	"zh-TW": "繁體中文",
}

var messages = map[string]map[string]string{
	"es-ES": esES,
	"ja-JP": jaJP,
	"zh-CN": zhCN,
	"zh-TW": zhTW,
}

func Normalize(lang string) string {
	lang = strings.TrimSpace(lang)
	if lang == "" {
		return DefaultLanguage
	}
	lang = strings.ReplaceAll(lang, "_", "-")
	lower := strings.ToLower(lang)
	if lower == "zh" || strings.HasPrefix(lower, "zh-cn") || strings.HasPrefix(lower, "zh-hans") {
		return "zh-CN"
	}
	if strings.HasPrefix(lower, "zh-tw") || strings.HasPrefix(lower, "zh-hk") || strings.HasPrefix(lower, "zh-mo") || strings.HasPrefix(lower, "zh-hant") {
		return "zh-TW"
	}
	if strings.HasPrefix(lower, "zh-") {
		return "zh-CN"
	}
	if lower == "ja" || strings.HasPrefix(lower, "ja-") {
		return "ja-JP"
	}
	if lower == "es" || strings.HasPrefix(lower, "es-") {
		return "es-ES"
	}
	return FallbackLanguage
}

func HTMLLang(lang string) string {
	return Normalize(lang)
}

func SupportedLanguages() []string {
	result := []string{FallbackLanguage}
	for lang := range messages {
		if lang != FallbackLanguage {
			result = append(result, lang)
		}
	}
	return result
}

func LanguageName(code string) string {
	if name, ok := languageNames[code]; ok {
		return name
	}
	return code
}

func T(lang, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	lang = Normalize(lang)
	if table, ok := messages[lang]; ok {
		if value, ok := table[key]; ok {
			return value
		}
	}
	return key
}
