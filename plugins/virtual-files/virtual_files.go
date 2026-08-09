package virtualfiles

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Chocola-X/GopherInk/core/plugin"
)

const (
	pluginName       = "virtual-files"
	filesPageName    = "files"
	filesConfigKey   = "files_json"
	defaultRobots    = "User-agent: *\nAllow: /\n"
	maxVirtualFiles  = 100
	maxFilePathBytes = 512
	maxFileBodyBytes = 512 << 10
	maxTotalBytes    = 4 << 20
)

type virtualFilesPlugin struct{}

type virtualFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func init() { plugin.Register(virtualFilesPlugin{}) }

func (virtualFilesPlugin) Name() string    { return pluginName }
func (virtualFilesPlugin) Version() string { return "0.5.0" }
func (virtualFilesPlugin) Description() string {
	return "Serve administrator-managed text files from fallback public paths for SEO and domain verification."
}

func (virtualFilesPlugin) Info() plugin.PluginInfo {
	return plugin.PluginInfo{
		Name:             pluginName,
		Version:          "0.5.0",
		Author:           "GopherInk",
		Description:      "Serve administrator-managed text files from fallback public paths for SEO and domain verification.",
		Homepage:         "https://gopherink.nekopara.uk",
		RequireGopherInk: "0.5.0",
	}
}

func (virtualFilesPlugin) Init(m *plugin.Manager) {
	m.RegisterPublicPathProvider(virtualFilePaths)
	m.RegisterRuntimeHook(plugin.HookRequestFallback, serveVirtualFile)
	m.RegisterAdminMenu(plugin.AdminMenuItem{
		Label: "Virtual Files",
		URL:   "/admin/plugins/" + pluginName + "/config?tab=" + filesPageName,
		Icon:  "description",
	})
}

func virtualFilePaths(ctx context.Context, rt *plugin.Runtime) ([]string, error) {
	files, err := runtimeFiles(ctx, rt)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(files))
	for filePath := range files {
		paths = append(paths, filePath)
	}
	sort.Strings(paths)
	return paths, nil
}

func (virtualFilesPlugin) Translate(lang, key string) string { return T(lang, key) }

func serveVirtualFile(ctx context.Context, rt *plugin.Runtime, value any) (any, error) {
	payload, ok := value.(plugin.RequestPayload)
	if !ok || payload.Handled || (payload.Method != http.MethodGet && payload.Method != http.MethodHead) {
		return value, nil
	}
	files, err := runtimeFiles(ctx, rt)
	if err != nil {
		return value, nil
	}
	file, exists := files[payload.Path]
	if !exists {
		return value, nil
	}
	payload.Handled = true
	payload.Status = http.StatusOK
	payload.ContentType = contentTypeForPath(file.Path)
	payload.Body = file.Content
	payload.ResponseHeaders = map[string]string{
		"Cache-Control":          "public, no-cache",
		"Content-Length":         strconv.Itoa(len(file.Content)),
		"X-Content-Type-Options": "nosniff",
	}
	return plugin.HookControl{Payload: payload, Stop: true}, nil
}

func runtimeFiles(ctx context.Context, rt *plugin.Runtime) (map[string]virtualFile, error) {
	if rt == nil || rt.Config == nil {
		return nil, plugin.ErrRuntimeUnavailable
	}
	values, err := rt.Config(ctx, pluginName)
	if err != nil {
		return nil, err
	}
	files, err := filesFromConfig(values)
	if err != nil {
		return nil, err
	}
	return fileMap(files), nil
}

func fileMap(files []virtualFile) map[string]virtualFile {
	result := make(map[string]virtualFile, len(files))
	for _, file := range files {
		result[file.Path] = file
	}
	return result
}

func filesFromConfig(values map[string]string) ([]virtualFile, error) {
	raw, exists := values[filesConfigKey]
	if !exists || strings.TrimSpace(raw) == "" {
		return defaultVirtualFiles(), nil
	}
	var files []virtualFile
	if err := json.Unmarshal([]byte(raw), &files); err != nil {
		return nil, errors.New("Stored virtual file data is invalid.")
	}
	for index := range files {
		if files[index].Path == "/robots.txt" && strings.TrimSpace(files[index].Content) == "" {
			files[index].Content = defaultRobots
		}
	}
	return validateVirtualFiles(files)
}

func encodeVirtualFiles(files []virtualFile) (string, error) {
	files, err := validateVirtualFiles(files)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(files)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func validateVirtualFiles(files []virtualFile) ([]virtualFile, error) {
	if len(files) > maxVirtualFiles {
		return nil, errors.New("At most 100 virtual files are allowed.")
	}
	result := make([]virtualFile, 0, len(files))
	seen := make(map[string]bool, len(files))
	total := 0
	for _, file := range files {
		normalized, err := normalizeVirtualPath(file.Path)
		if err != nil {
			return nil, err
		}
		if seen[normalized] {
			return nil, errors.New("A virtual file with this path already exists.")
		}
		if len(file.Content) > maxFileBodyBytes {
			return nil, errors.New("A virtual file cannot exceed 512 KiB.")
		}
		total += len(file.Content)
		if total > maxTotalBytes {
			return nil, errors.New("Virtual file content cannot exceed 4 MiB in total.")
		}
		seen[normalized] = true
		result = append(result, virtualFile{Path: normalized, Content: normalizeNewlines(file.Content)})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func normalizeVirtualPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxFilePathBytes || !utf8.ValidString(value) || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") {
		return "", errors.New("Enter a valid absolute path beginning with /.")
	}
	if strings.ContainsAny(value, "\\?#%") || path.Clean(value) != value || strings.HasSuffix(value, "/") {
		return "", errors.New("The path must be canonical and cannot contain a query, fragment, percent escape, or trailing slash.")
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return "", errors.New("The path contains unsupported characters.")
		}
	}
	name := path.Base(value)
	if name == "." || name == ".." || path.Ext(name) == "" {
		return "", errors.New("The path must include a filename and extension.")
	}
	return value, nil
}

func defaultVirtualFiles() []virtualFile {
	return []virtualFile{{Path: "/robots.txt", Content: defaultRobots}}
}

func normalizeNewlines(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
}

func contentTypeForPath(filename string) string {
	switch strings.ToLower(path.Ext(filename)) {
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".xml":
		return "application/xml; charset=utf-8"
	case ".json", ".webmanifest":
		return "application/json; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".svg":
		return "image/svg+xml; charset=utf-8"
	}
	if detected := mime.TypeByExtension(strings.ToLower(path.Ext(filename))); strings.HasPrefix(detected, "text/") {
		if !strings.Contains(strings.ToLower(detected), "charset=") {
			detected += "; charset=utf-8"
		}
		return detected
	}
	return "text/plain; charset=utf-8"
}
