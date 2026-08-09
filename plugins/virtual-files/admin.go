package virtualfiles

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	htmltemplate "html/template"

	"github.com/Chocola-X/GopherInk/core/plugin"
)

type virtualFilesPageData struct {
	CSRF  string
	Lang  string
	Files []virtualFile
}

func (virtualFilesPlugin) AdminPages() []plugin.AdminPage {
	return []plugin.AdminPage{{
		Name:        filesPageName,
		Label:       "Files",
		Icon:        "description",
		Title:       "Virtual files",
		Description: "Manage small text responses used by robots, SEO tools, and domain ownership verification.",
	}}
}

func (virtualFilesPlugin) RenderAdminPage(ctx context.Context, rt *plugin.Runtime, page string, rc plugin.AdminPageRenderContext) (htmltemplate.HTML, error) {
	lang := runtimeLanguage(ctx, rt)
	if page != filesPageName {
		return "", errors.New(T(lang, "Unknown plugin page."))
	}
	files, err := filesFromConfig(rc.Config)
	if err != nil {
		return "", err
	}
	var output bytes.Buffer
	if err := virtualFilesTemplate.Execute(&output, virtualFilesPageData{CSRF: rc.CSRF, Lang: lang, Files: files}); err != nil {
		return "", fmt.Errorf("render virtual files: %w", err)
	}
	return htmltemplate.HTML(output.String()), nil
}

func (virtualFilesPlugin) HandleAdminPageAction(ctx context.Context, rt *plugin.Runtime, page string, form map[string][]string) (plugin.AdminPageActionResult, error) {
	lang := runtimeLanguage(ctx, rt)
	if page != filesPageName {
		return plugin.AdminPageActionResult{}, errors.New(T(lang, "Unknown plugin page."))
	}
	if rt == nil || rt.Config == nil {
		return plugin.AdminPageActionResult{}, plugin.ErrRuntimeUnavailable
	}
	values, err := rt.Config(ctx, pluginName)
	if err != nil {
		return plugin.AdminPageActionResult{}, err
	}
	files, err := filesFromConfig(values)
	if err != nil {
		return plugin.AdminPageActionResult{}, err
	}
	action := firstFormValue(form, "action")
	switch action {
	case "add":
		if len(files) >= maxVirtualFiles {
			return plugin.AdminPageActionResult{}, errors.New(T(lang, "At most 100 virtual files are allowed."))
		}
		file, err := virtualFileFromForm(form)
		if err != nil {
			return plugin.AdminPageActionResult{}, errors.New(T(lang, err.Error()))
		}
		for _, existing := range files {
			if existing.Path == file.Path {
				return plugin.AdminPageActionResult{}, errors.New(T(lang, "A virtual file with this path already exists."))
			}
		}
		files = append(files, file)
	case "save":
		oldPath, err := normalizeVirtualPath(firstFormValue(form, "old_path"))
		if err != nil {
			return plugin.AdminPageActionResult{}, errors.New(T(lang, err.Error()))
		}
		file, err := virtualFileFromForm(form)
		if err != nil {
			return plugin.AdminPageActionResult{}, errors.New(T(lang, err.Error()))
		}
		found := false
		for index := range files {
			if files[index].Path == file.Path && files[index].Path != oldPath {
				return plugin.AdminPageActionResult{}, errors.New(T(lang, "A virtual file with this path already exists."))
			}
			if files[index].Path == oldPath {
				files[index] = file
				found = true
			}
		}
		if !found {
			return plugin.AdminPageActionResult{}, errors.New(T(lang, "The virtual file no longer exists."))
		}
	case "delete":
		oldPath, err := normalizeVirtualPath(firstFormValue(form, "old_path"))
		if err != nil {
			return plugin.AdminPageActionResult{}, errors.New(T(lang, err.Error()))
		}
		filtered := files[:0]
		for _, file := range files {
			if file.Path != oldPath {
				filtered = append(filtered, file)
			}
		}
		if len(filtered) == len(files) {
			return plugin.AdminPageActionResult{}, errors.New(T(lang, "The virtual file no longer exists."))
		}
		files = filtered
	default:
		return plugin.AdminPageActionResult{}, errors.New(T(lang, "Unsupported action."))
	}

	encoded, err := encodeVirtualFiles(files)
	if err != nil {
		return plugin.AdminPageActionResult{}, errors.New(T(lang, err.Error()))
	}
	message := "Virtual file saved."
	if action == "delete" {
		message = "Virtual file deleted."
	}
	return plugin.AdminPageActionResult{
		ConfigPatch: map[string]string{filesConfigKey: encoded},
		Notice: plugin.AdminNotice{
			Type: plugin.NoticeSuccess, Mode: plugin.NoticeSnackbar, Message: message,
		},
	}, nil
}

func virtualFileFromForm(form map[string][]string) (virtualFile, error) {
	filePath, err := normalizeVirtualPath(firstFormValue(form, "path"))
	if err != nil {
		return virtualFile{}, err
	}
	content := normalizeNewlines(firstFormValue(form, "content"))
	if len(content) > maxFileBodyBytes {
		return virtualFile{}, errors.New("A virtual file cannot exceed 512 KiB.")
	}
	return virtualFile{Path: filePath, Content: content}, nil
}

func firstFormValue(form map[string][]string, name string) string {
	values := form[name]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func runtimeLanguage(ctx context.Context, rt *plugin.Runtime) string {
	if rt != nil && rt.Language != nil {
		return rt.Language(ctx)
	}
	return "en-US"
}

var virtualFilesTemplate = htmltemplate.Must(htmltemplate.New("virtual-files").Funcs(htmltemplate.FuncMap{"T": T}).Parse(`
<div class="vf-panel" data-virtual-files-panel>
  <div class="vf-toolbar">
    <p class="muted">{{T .Lang "Configured files are only served after all CMS, theme, and plugin routes fail to match. Existing routes always take priority."}}</p>
    <mdui-button type="button" data-vf-open-add><mdui-icon slot="icon" name="add"></mdui-icon>{{T .Lang "Add virtual file"}}</mdui-button>
  </div>

  <div class="vf-list" data-vf-list>
    <template data-vf-new-card-template>
      <form method="post" class="vf-card vf-card-new" data-vf-new-card>
        <input type="hidden" name="_csrf" value="{{.CSRF}}">
        <input type="hidden" name="action" value="add">
        <div class="vf-card-head">
          <span class="vf-file-icon"><mdui-icon name="note_add"></mdui-icon></span>
          <strong>{{T .Lang "New virtual file"}}</strong>
        </div>
        <mdui-text-field name="path" label="{{T .Lang "Virtual path"}}" placeholder="/verification/example.txt" required></mdui-text-field>
        <mdui-text-field name="content" label="{{T .Lang "Text content"}}" placeholder="{{T .Lang "Enter the text file content"}}" rows="10" autosize required></mdui-text-field>
        <div class="vf-card-actions">
          <mdui-button type="button" variant="text" data-vf-discard-new><mdui-icon slot="icon" name="close"></mdui-icon>{{T .Lang "Cancel"}}</mdui-button>
          <mdui-button type="submit"><mdui-icon slot="icon" name="save"></mdui-icon>{{T .Lang "Save"}}</mdui-button>
        </div>
      </form>
    </template>
    {{if .Files}}
    {{range .Files}}
    <form method="post" class="vf-card">
      <input type="hidden" name="_csrf" value="{{$.CSRF}}">
      <input type="hidden" name="action" value="save">
      <input type="hidden" name="old_path" value="{{.Path}}">
      <div class="vf-card-head">
        <span class="vf-file-icon"><mdui-icon name="description"></mdui-icon></span>
        <a href="{{.Path}}" target="_blank" rel="noopener noreferrer">{{.Path}}</a>
      </div>
      <mdui-text-field name="path" label="{{T $.Lang "Virtual path"}}" value="{{.Path}}" required></mdui-text-field>
      <mdui-text-field name="content" label="{{T $.Lang "Text content"}}" value="{{.Content}}" rows="10" autosize required></mdui-text-field>
      <div class="vf-card-actions">
        <mdui-button type="button" variant="text" class="vf-delete" data-vf-delete-path="{{.Path}}"><mdui-icon slot="icon" name="delete"></mdui-icon>{{T $.Lang "Delete"}}</mdui-button>
        <mdui-button type="submit"><mdui-icon slot="icon" name="save"></mdui-icon>{{T $.Lang "Save"}}</mdui-button>
      </div>
    </form>
    {{end}}
    {{else}}
    <div class="vf-empty" data-vf-empty><mdui-icon name="description"></mdui-icon><span>{{T .Lang "No virtual files are configured."}}</span></div>
    {{end}}
  </div>

  <mdui-dialog class="vf-delete-dialog" close-on-esc close-on-overlay-click headline="{{T .Lang "Delete virtual file?"}}" description="{{T .Lang "The configured response will stop being available immediately."}}">
    <form method="post" id="vf-delete-form">
      <input type="hidden" name="_csrf" value="{{.CSRF}}">
      <input type="hidden" name="action" value="delete">
      <input type="hidden" name="old_path" value="">
      <button type="submit" hidden data-vf-native-delete-submit></button>
    </form>
    <mdui-button slot="action" type="button" variant="text" data-vf-close-delete>{{T .Lang "Cancel"}}</mdui-button>
    <mdui-button slot="action" type="button" data-vf-submit-delete class="vf-danger">{{T .Lang "Delete"}}</mdui-button>
  </mdui-dialog>
</div>

<style>
.vf-panel{width:100%}.vf-toolbar{display:flex;align-items:center;justify-content:space-between;gap:18px;margin-bottom:18px}.vf-toolbar p{max-width:760px;margin:0}.vf-list{display:grid;gap:14px}.vf-card{display:grid;gap:16px;box-sizing:border-box;width:100%;padding:18px;border:1px solid rgba(var(--mdui-color-outline-variant),.72);border-radius:8px;background:rgba(var(--mdui-color-surface-container),var(--admin-card-opacity,.84));box-shadow:0 6px 20px rgba(15,23,42,.12)}.vf-card-new{border-color:rgb(var(--mdui-color-primary))}.vf-card-head{display:flex;align-items:center;gap:10px;min-width:0}.vf-card-head a{min-width:0;color:rgb(var(--mdui-color-primary));font-weight:600;overflow-wrap:anywhere}.vf-file-icon{display:grid;flex:0 0 34px;width:34px;height:34px;place-items:center;border-radius:50%;color:rgb(var(--mdui-color-on-primary-container));background:rgb(var(--mdui-color-primary-container))}.vf-file-icon mdui-icon{font-size:20px}.vf-card mdui-text-field{width:100%}.vf-card-actions{display:flex;justify-content:flex-end;gap:8px}.vf-delete,.vf-danger{--mdui-color-primary:var(--mdui-color-error)}.vf-empty{display:flex;align-items:center;justify-content:center;gap:10px;min-height:160px;padding:20px;border:1px dashed rgb(var(--mdui-color-outline));border-radius:8px;color:rgb(var(--mdui-color-on-surface-variant))}
@media(max-width:600px){.vf-toolbar{align-items:stretch;flex-direction:column}.vf-toolbar mdui-button{width:100%}.vf-card{padding:14px}.vf-card-actions mdui-button{flex:1}}
</style>
<script>
(()=>{const root=document.querySelector('[data-virtual-files-panel]');if(!root||root.dataset.bound==='1')return;root.dataset.bound='1';const list=root.querySelector('[data-vf-list]');const cardTemplate=root.querySelector('[data-vf-new-card-template]');const remove=root.querySelector('.vf-delete-dialog');const removeForm=root.querySelector('#vf-delete-form');const bindSavedCard=card=>card.querySelector('[data-vf-delete-path]')?.addEventListener('click',()=>{removeForm.querySelector('[name="old_path"]').value=card.querySelector('[name="old_path"]').value||'';remove.open=true});root.querySelectorAll('.vf-card:not([data-vf-new-card])').forEach(bindSavedCard);root.querySelector('[data-vf-open-add]')?.addEventListener('click',()=>{let card=list.querySelector('[data-vf-new-card]');if(!card){list.prepend(cardTemplate.content.cloneNode(true));card=list.querySelector('[data-vf-new-card]');root.querySelector('[data-vf-empty]')?.setAttribute('hidden','');card.querySelector('[data-vf-discard-new]')?.addEventListener('click',()=>{card.remove();root.querySelector('[data-vf-empty]')?.removeAttribute('hidden')})}card.scrollIntoView({behavior:'smooth',block:'start'});requestAnimationFrame(()=>card.querySelector('[name="path"]')?.focus())});root.querySelector('[data-vf-close-delete]')?.addEventListener('click',()=>{remove.open=false});root.querySelector('[data-vf-submit-delete]')?.addEventListener('click',()=>removeForm.querySelector('[data-vf-native-delete-submit]').click());})();
</script>`))
