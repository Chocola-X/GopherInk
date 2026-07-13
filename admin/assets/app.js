(function () {
  var adminDirty = false;
  var pjaxAbort = null;
  var pjaxReady = false;
  var lastSubmitter = null;
  var lastEditorSelection = null;
  var markdownEditorLoading = null;

  function ready(fn) {
    if (document.readyState === "loading") {
      document.addEventListener("DOMContentLoaded", fn);
    } else {
      fn();
    }
  }

  ready(function () {
    applyAdminAppearance();
    initAdminChrome();
    initAdminPage(document);
    initAdminPjax();
    updateActiveNav(window.location.href);

    window.addEventListener("beforeunload", function (event) {
      if (!adminDirty) {
        return;
      }
      event.preventDefault();
      event.returnValue = "";
    });
  });

  function applyAdminAppearance() {
    var body = document.body;
    if (!body) {
      return;
    }

    var primary = body.dataset.adminPrimary || "#6750a4";
    if (window.mdui && typeof window.mdui.setColorScheme === "function") {
      try {
        window.mdui.setColorScheme(primary);
      } catch (err) {
        window.mdui.setColorScheme("#6750a4");
      }
    }

    var desktopBg = body.dataset.adminBackground || "";
    var mobileBg = body.dataset.adminMobileBackground || "";
    if (desktopBg) {
      body.style.setProperty("--admin-bg-image", cssURL(desktopBg));
    } else {
      body.style.removeProperty("--admin-bg-image");
    }
    if (mobileBg) {
      body.style.setProperty("--admin-mobile-bg-image", cssURL(mobileBg));
    } else {
      body.style.removeProperty("--admin-mobile-bg-image");
    }
  }

  function initAdminChrome() {
    var drawer = document.querySelector(".admin-drawer");
    var toggle = document.querySelector(".drawer-toggle");
    var scrim = document.querySelector(".drawer-scrim");

    if (!drawer || !toggle || !scrim || toggle.dataset.adminDrawerBound) {
      return;
    }

    function setDrawer(open, persist) {
      if (open) {
        drawer.setAttribute("open", "");
      } else {
        drawer.removeAttribute("open");
      }
      document.body.classList.toggle("admin-drawer-open", open);
      document.body.classList.remove("admin-drawer-modal");
      if (persist !== false) {
        localStorage.setItem("goblogAdminDrawerOpen", open ? "1" : "0");
      }
    }

    var stored = localStorage.getItem("goblogAdminDrawerOpen");
    var defaultOpen = window.matchMedia("(min-width: 920px)").matches;
    setDrawer(stored === null ? defaultOpen : stored === "1", false);

    toggle.dataset.adminDrawerBound = "1";
    toggle.addEventListener("click", function () {
      setDrawer(!drawer.hasAttribute("open"));
    });

    scrim.addEventListener("click", function () {
      setDrawer(false);
    });

    window.addEventListener("keydown", function (event) {
      if (event.key === "Escape" && drawer.hasAttribute("open")) {
        setDrawer(false);
      }
    });
  }

  function initAdminPage(root) {
    adminDirty = false;
    ensureCSRF(root);
    initAutosave(root);
    initCustomFields(root);
    initTagInputs(root);
    initMediaPicker(root);
    initEditorUpload(root);
    initMarkdownEditor(root);
    initNativeFileButtons(root);
    initCopyButtons(root);
    initSchemaForm(root);
    initImageProcessingOptions(root);
    initAdminNotices(root);
  }

  function ensureCSRF(root) {
    var csrf = csrfToken();
    if (!csrf) {
      return;
    }
    query(root, "form").forEach(function (form) {
      if ((form.getAttribute("method") || "").toLowerCase() !== "post") {
        return;
      }
      var hidden = form.querySelector('input[name="_csrf"]');
      if (hidden) {
        hidden.value = csrf;
      } else {
        var input = document.createElement("input");
        input.type = "hidden";
        input.name = "_csrf";
        input.value = csrf;
        form.appendChild(input);
      }
    });
  }

  function initMarkdownEditor(root) {
    if (!(root || document).querySelector("[data-markdown-editor]")) {
      return;
    }
    if (window.GoBlogMarkdownEditor && typeof window.GoBlogMarkdownEditor.init === "function") {
      window.GoBlogMarkdownEditor.init(root || document);
      return;
    }
    if (!markdownEditorLoading) {
      markdownEditorLoading = new Promise(function (resolve, reject) {
        var script = document.createElement("script");
        script.src = "/admin/assets/markdown-editor.js";
        script.defer = true;
        script.onload = resolve;
        script.onerror = reject;
        document.head.appendChild(script);
      });
    }
    markdownEditorLoading.then(function () {
      if (window.GoBlogMarkdownEditor && typeof window.GoBlogMarkdownEditor.init === "function") {
        window.GoBlogMarkdownEditor.init(root || document);
      }
    }).catch(function () {
      showMessage("Markdown 编辑器加载失败");
    });
  }

  function initAutosave(root) {
    query(root, ".field-stack").forEach(function (form) {
      if (!form.matches("form") || (form.getAttribute("method") || "").toLowerCase() !== "post") {
        return;
      }
      if (bound(form, "adminAutosaveBound")) {
        return;
      }

      var cidInput = form.querySelector('input[name="cid"]');
      var typeInput = form.querySelector('input[name="type"]');
      var status = form.querySelector(".autosave-status");
      var timer = null;
      var saving = false;
      var pending = false;
      var submitting = false;

      function markClean() {
        adminDirty = false;
      }

      function hasContentID() {
        return cidInput && parseInt(cidInput.value || "0", 10) > 0;
      }

      function updateContentID(id) {
        id = parseInt(id || "0", 10);
        if (id > 0) {
          var changed = cidInput.value !== String(id);
          cidInput.value = String(id);
          if (changed) {
            document.dispatchEvent(new CustomEvent("goblog:content-id", { detail: { cid: id } }));
          }
        }
      }

      function sendAutosave() {
        if (submitting) {
          return;
        }
        if (saving) {
          pending = true;
          return;
        }
        saving = true;
        var data = new FormData(form);
        data.set("_csrf", csrfToken());
        fetch("/admin/autosave", {
          method: "POST",
          body: data,
          credentials: "same-origin"
        }).then(function (res) {
          if (!res.ok) {
            throw new Error("autosave failed");
          }
          return res.json();
        }).then(function (payload) {
          updateContentID(payload.cid);
          markClean();
          if (status) {
            status.textContent = "已自动保存";
          }
        }).catch(function () {
          if (status) {
            status.textContent = "自动保存失败";
          }
        }).finally(function () {
          saving = false;
          if (pending && !submitting) {
            pending = false;
            scheduleAutosave();
          }
        });
      }

      function scheduleAutosave() {
        var csrf = csrfToken();
        if (!cidInput || !typeInput || !csrf) {
          return;
        }
        adminDirty = true;
        if (status) {
          status.textContent = "有未保存修改";
        }
        clearTimeout(timer);
        timer = setTimeout(function () {
          sendAutosave();
        }, 3000);
      }

      form.addEventListener("input", scheduleAutosave);
      form.addEventListener("change", scheduleAutosave);
      form.addEventListener("submit", function (event) {
        clearTimeout(timer);
        if (saving && !hasContentID()) {
          event.preventDefault();
          pending = false;
          if (status) {
            status.textContent = "正在完成自动保存";
          }
          var retry = setInterval(function () {
            if (saving) {
              return;
            }
            clearInterval(retry);
            submitting = true;
            if (form.requestSubmit) {
              form.requestSubmit();
            } else {
              form.submit();
            }
          }, 100);
          return;
        }
        submitting = true;
        markClean();
      });
    });
  }

  function initCustomFields(root) {
    query(root, ".custom-fields").forEach(function (fieldset) {
      if (bound(fieldset, "adminCustomFieldsBound")) {
        return;
      }
      var list = fieldset.querySelector(".custom-field-list");
      var add = fieldset.querySelector(".add-field");
      if (!list || !add) {
        return;
      }

      function row() {
        var wrapper = document.createElement("div");
        wrapper.className = "custom-field-row";
        wrapper.innerHTML = '<mdui-text-field name="field_name" label="字段名"></mdui-text-field><mdui-select name="field_type" label="类型" value="str"><mdui-menu-item value="str">字符串</mdui-menu-item><mdui-menu-item value="int">整数</mdui-menu-item><mdui-menu-item value="float">浮点数</mdui-menu-item><mdui-menu-item value="json">JSON</mdui-menu-item></mdui-select><mdui-text-field name="field_value" label="值"></mdui-text-field><mdui-button-icon type="button" class="remove-field" title="删除字段"><mdui-icon name="delete"></mdui-icon></mdui-button-icon>';
        return wrapper;
      }

      add.addEventListener("click", function () {
        list.appendChild(row());
      });
      list.addEventListener("click", function (event) {
        var remove = event.target.closest(".remove-field");
        if (remove) {
          remove.closest(".custom-field-row").remove();
        }
      });
    });
  }

  function initTagInputs(root) {
    query(root, ".tag-input").forEach(function (input) {
      if (bound(input, "adminTagInputBound")) {
        return;
      }
      var last = "";
      input.addEventListener("input", function () {
        var value = input.value || "";
        var term = value.split(/[,，]/).pop().trim();
        if (!term || term === last) {
          return;
        }
        last = term;
        fetch("/admin/tags/search?q=" + encodeURIComponent(term), { credentials: "same-origin" })
          .then(function (res) { return res.ok ? res.json() : []; })
          .then(function (items) {
            if (!items.length) {
              return;
            }
            input.helper = "建议：" + items.map(function (item) { return item.name; }).join("，");
          }).catch(function () {});
      });
    });
  }

  function initMediaPicker(root) {
    var editor = document.querySelector("#content-text");
    if (editor && !editor.dataset.adminEditorSelectionBound) {
      editor.dataset.adminEditorSelectionBound = "1";
      ["focus", "click", "keyup", "mouseup", "select", "input"].forEach(function (eventName) {
        editor.addEventListener(eventName, function () {
          rememberEditorSelection(editor);
        });
      });
    }

    query(root, ".media-picker").forEach(function (picker) {
      if (bound(picker, "adminMediaPickerBound")) {
        return;
      }
      picker.addEventListener("click", function (event) {
        var copy = event.target.closest(".media-url-copy");
        if (copy && picker.contains(copy)) {
          copyMediaURL(copy);
          return;
        }
        var button = event.target.closest(".media-pick");
        if (!button || !picker.contains(button)) {
          return;
        }
        appendToEditor(button.dataset.markdown || "");
      });
    });

    query(root, ".editor-media-current").forEach(function (panel) {
      if (bound(panel, "adminCurrentMediaBound")) {
        return;
      }
      loadCurrentEditorMedia(panel);
      document.addEventListener("goblog:content-id", function (event) {
        if (!document.documentElement.contains(panel)) {
          return;
        }
        if (event.detail && event.detail.cid) {
          panel.dataset.parentId = String(event.detail.cid);
          loadCurrentEditorMedia(panel);
        }
      });
      document.addEventListener("goblog:media-uploaded", function () {
        if (!document.documentElement.contains(panel)) {
          return;
        }
        loadCurrentEditorMedia(panel);
      });
    });

    query(root, ".editor-media-library").forEach(function (panel) {
      if (bound(panel, "adminLibraryMediaBound")) {
        return;
      }
      var select = panel.querySelector(".media-source-select");
      if (!select) {
        return;
      }
      select.addEventListener("change", function () {
        loadLibraryEditorMedia(panel);
      });
      loadLibraryEditorMedia(panel);
    });
  }

  function rememberEditorSelection(editor) {
    if (!editor || typeof editor.selectionStart !== "number") {
      return;
    }
    lastEditorSelection = {
      editor: editor,
      start: editor.selectionStart,
      end: editor.selectionEnd
    };
  }

  function appendToEditor(text) {
    var editor = document.querySelector("#content-text");
    if (!editor || !text) {
      return;
    }
    var current = editor.value || "";
    var start = current.length;
    var end = current.length;
    if (document.activeElement === editor && typeof editor.selectionStart === "number") {
      start = editor.selectionStart;
      end = editor.selectionEnd;
    }
    if (lastEditorSelection && lastEditorSelection.editor === editor) {
      start = lastEditorSelection.start;
      end = lastEditorSelection.end;
    }
    start = Math.max(0, Math.min(start, current.length));
    end = Math.max(start, Math.min(end, current.length));
    var prefix = start > 0 && current.charAt(start - 1) !== "\n" ? "\n\n" : "";
    var suffix = end < current.length && current.charAt(end) !== "\n" ? "\n\n" : "\n";
    var insert = prefix + text + suffix;
    editor.value = current.slice(0, start) + insert + current.slice(end);
    var nextCursor = start + insert.length;
    editor.dispatchEvent(new Event("input", { bubbles: true }));
    editor.focus();
    if (typeof editor.setSelectionRange === "function") {
      editor.setSelectionRange(nextCursor, nextCursor);
      rememberEditorSelection(editor);
    }
  }

  function loadCurrentEditorMedia(panel) {
    var parentID = parseInt(panel.dataset.parentId || "0", 10);
    var picker = panel.querySelector(".media-picker-current");
    if (!picker) {
      return;
    }
    if (!parentID) {
      renderMediaItems(picker, [], picker.dataset.emptyText || "当前内容暂无附件。");
      return;
    }
    loadEditorMedia(panel.dataset.editorMediaUrl || "/admin/medias/editor", {
      source: "current",
      parent: String(parentID)
    }, picker, picker.dataset.emptyText || "当前内容暂无附件。");
  }

  function loadLibraryEditorMedia(panel) {
    var picker = panel.querySelector(".media-picker-library");
    var select = panel.querySelector(".media-source-select");
    if (!picker || !select) {
      return;
    }
    var source = select.value || "__none";
    var emptyText = source === "__none" ? "请选择附件来源。" : "该来源暂无附件。";
    if (source === "__none") {
      renderMediaItems(picker, [], emptyText);
      return;
    }
    loadEditorMedia(panel.dataset.editorMediaUrl || "/admin/medias/editor", {
      source: source
    }, picker, emptyText);
  }

  function loadEditorMedia(url, params, picker, emptyText) {
    picker.innerHTML = '<span class="muted">加载中...</span>';
    var query = new URLSearchParams(params);
    fetch(url + "?" + query.toString(), {
      headers: { "Accept": "application/json" },
      credentials: "same-origin"
    }).then(function (res) {
      if (!res.ok) {
        throw new Error("media load failed");
      }
      return res.json();
    }).then(function (payload) {
      renderMediaItems(picker, payload.items || [], emptyText);
    }).catch(function () {
      renderMediaItems(picker, [], "附件加载失败。");
    });
  }

  function renderMediaItems(picker, items, emptyText) {
    if (!items.length) {
      picker.innerHTML = '<span class="muted">' + escapeHTML(emptyText || "暂无可插入附件。") + '</span>';
      return;
    }
    picker.innerHTML = items.map(mediaItemHTML).join("");
  }

  function mediaItemHTML(item) {
    var title = "插入 " + (item.name || item.url || "附件");
    var meta = [item.sizeLabel || "", item.mime || ""].filter(Boolean).join(" · ");
    var preview = item.isImage && (item.thumbnailURL || item.url)
      ? '<img src="' + escapeHTML(item.thumbnailURL || item.url) + '" alt="">'
      : '<span class="media-file-icon"><mdui-icon name="' + escapeHTML(item.icon || "insert_drive_file") + '"></mdui-icon></span>';
    return '<div class="media-pick-card media-pick-' + escapeHTML(item.kind || "file") + '">' +
      '<button type="button" class="media-pick" data-markdown="' + escapeHTML(item.markdown || item.url || "") + '" title="' + escapeHTML(title) + '">' +
        preview +
        '<span class="media-name">' + escapeHTML(item.name || item.url || "附件") + '</span>' +
        (meta ? '<small>' + escapeHTML(meta) + '</small>' : '') +
      '</button>' +
      '<div class="media-url-actions">' +
        '<mdui-button type="button" variant="text" class="media-url-copy" data-copy-url="' + escapeHTML(item.absoluteURL || item.url || "") + '">绝对URL</mdui-button>' +
        '<mdui-button type="button" variant="text" class="media-url-copy" data-copy-url="' + escapeHTML(item.relativeURL || item.url || "") + '">相对URL</mdui-button>' +
      '</div>' +
      '</div>';
  }

  function copyMediaURL(button) {
    var value = button.dataset.copyUrl || "";
    if (!value) {
      return;
    }
    copyToClipboard(value).then(function () {
      showMessage("URL 已复制", { type: "info" });
    }).catch(function () {
      showMessage("复制失败");
    });
  }

  function ensureEditorContentID(form) {
    var cidInput = form && form.querySelector('input[name="cid"]');
    var cid = cidInput ? parseInt(cidInput.value || "0", 10) : 0;
    if (cid > 0) {
      return Promise.resolve(cid);
    }
    if (!form) {
      return Promise.resolve(0);
    }
    var data = new FormData(form);
    data.set("_csrf", csrfToken());
    return fetch("/admin/autosave", {
      method: "POST",
      body: data,
      credentials: "same-origin"
    }).then(function (res) {
      if (!res.ok) {
        throw new Error("autosave failed");
      }
      return res.json();
    }).then(function (payload) {
      var nextID = parseInt(payload.cid || "0", 10);
      if (cidInput && nextID > 0) {
        cidInput.value = String(nextID);
        document.dispatchEvent(new CustomEvent("goblog:content-id", { detail: { cid: nextID } }));
      }
      return nextID;
    });
  }

  function initEditorUpload(root) {
    query(root, ".editor-upload").forEach(function (panel) {
      if (bound(panel, "adminEditorUploadBound")) {
        return;
      }
      var fileField = panel.querySelector(".editor-upload-file");
      var button = panel.querySelector(".editor-upload-button");
      var cid = document.querySelector('input[name="cid"]');
      if (!fileField || !button) {
        return;
      }

      button.addEventListener("click", function () {
        fileField.value = "";
        fileField.click();
      });

      fileField.addEventListener("change", function () {
        var csrf = csrfToken();
        var file = fileField.files && fileField.files.length ? fileField.files[0] : null;
        var form = panel.closest("form");
        if (!csrf) {
          setButtonLabel(button, "令牌失效");
          return;
        }
        if (!file) {
          return;
        }
        button.loading = true;
        setButtonLabel(button, "准备草稿");
        ensureEditorContentID(form).then(function (contentID) {
          var data = new FormData();
          data.set("_csrf", csrf);
          data.set("file", file);
          if (contentID > 0) {
            data.set("cid", String(contentID));
          } else if (cid && cid.value) {
            data.set("cid", cid.value);
          }
          setButtonLabel(button, "上传中");
          return fetch("/admin/medias", {
            method: "POST",
            body: data,
            headers: { "Accept": "application/json" },
            credentials: "same-origin"
          });
        }).then(function (res) {
          if (!res.ok) {
            throw new Error("upload failed");
          }
          return res.json();
        }).then(function (payload) {
          appendToEditor(payload.markdown || payload.url || "");
          document.dispatchEvent(new CustomEvent("goblog:media-uploaded", { detail: payload }));
          setButtonLabel(button, "已插入");
          if (payload.warning) {
            showMessage(payload.warning, { type: "info" });
          }
        }).catch(function (err) {
          showMessage("上传失败：" + err.message);
          setButtonLabel(button, "上传并插入");
        }).finally(function () {
          fileField.value = "";
          button.loading = false;
        });
      });
    });
  }

  function initNativeFileButtons(root) {
    query(root, ".file-select-button").forEach(function (button) {
      if (bound(button, "adminFileButtonBound")) {
        return;
      }
      button.addEventListener("click", function () {
        var id = button.dataset.fileTarget || "";
        var input = id ? document.getElementById(id) : null;
        if (!input) {
          input = button.closest("form") && button.closest("form").querySelector(".native-file-input");
        }
        if (!input) {
          return;
        }
        input.value = "";
        input.click();
      });
    });

    query(root, ".native-file-input").forEach(function (input) {
      if (bound(input, "adminNativeFileBound")) {
        return;
      }
      input.addEventListener("change", function () {
        var file = input.files && input.files.length ? input.files[0] : null;
        var control = input.closest(".file-control") || input.closest(".inline-upload");
        var label = control && control.querySelector(".file-name");
        if (label) {
          label.textContent = file ? file.name : "未选择文件";
        }
        if (file && input.hasAttribute("data-auto-submit") && input.form) {
          if (typeof input.form.requestSubmit === "function") {
            input.form.requestSubmit();
          } else {
            input.form.submit();
          }
        }
      });
    });
  }

  function initCopyButtons(root) {
    query(root, ".copy-text").forEach(function (button) {
      if (bound(button, "adminCopyBound")) {
        return;
      }
      button.addEventListener("click", function () {
        var value = button.dataset.copy || "";
        if (!value) {
          return;
        }
        copyToClipboard(value).then(function () {
          if (button.classList.contains("media-copy-button") || button.classList.contains("copy-notice-button")) {
            showMessage("URL 已复制", { type: "info" });
          } else {
            button.textContent = "已复制";
          }
        }).catch(function () {});
      });
    });
  }

  function copyToClipboard(value) {
    if (navigator.clipboard && typeof navigator.clipboard.writeText === "function") {
      return navigator.clipboard.writeText(value);
    }
    return new Promise(function (resolve, reject) {
      var input = document.createElement("textarea");
      input.value = value;
      input.setAttribute("readonly", "");
      input.style.position = "fixed";
      input.style.left = "-9999px";
      document.body.appendChild(input);
      input.select();
      try {
        document.execCommand("copy") ? resolve() : reject(new Error("copy failed"));
      } catch (err) {
        reject(err);
      } finally {
        input.remove();
      }
    });
  }

  function initSchemaForm(root) {
    query(root, "form[data-schema-upload-url]").forEach(function (form) {
      if (bound(form, "adminSchemaFormBound")) {
        return;
      }
      var uploadURL = form.dataset.schemaUploadUrl || "/admin/schema/upload";

      query(form, ".schema-upload-button").forEach(function (button) {
        button.addEventListener("click", function () {
          var field = button.closest(".schema-image-field");
          var fileInput = field && field.querySelector(".schema-native-file");
          if (fileInput) {
            fileInput.value = "";
            fileInput.click();
          }
        });
      });

      query(form, ".schema-native-file").forEach(function (input) {
        input.addEventListener("change", function () {
          var field = input.closest(".schema-image-field");
          var button = field && field.querySelector(".schema-upload-button");
          var file = input.files && input.files.length ? input.files[0] : null;
          if (!button || !file) {
            return;
          }
          var data = new FormData();
          data.append("file", file);
          var csrf = csrfToken();
          if (csrf) {
            data.append("_csrf", csrf);
          }
          button.loading = true;
          setButtonLabel(button, "上传中");
          fetch(uploadURL, { method: "POST", body: data, credentials: "same-origin" })
            .then(function (response) {
              if (!response.ok) {
                return response.text().then(function (text) {
                  throw new Error(text || "upload failed");
                });
              }
              return response.json();
            })
            .then(function (result) {
              var target = form.querySelector('[name="' + cssEscape(button.dataset.target || "") + '"]');
              if (target && result.url) {
                target.value = result.url;
                target.dispatchEvent(new Event("input", { bubbles: true }));
              }
              setButtonLabel(button, "已填入");
              if (result.warning) {
                showMessage(result.warning, { type: "info" });
              }
            })
            .catch(function (err) {
              showMessage("上传失败：" + err.message);
              setButtonLabel(button, "上传并填入");
            })
            .finally(function () {
              input.value = "";
              button.loading = false;
            });
        });
      });
    });
  }

  function initImageProcessingOptions(root) {
    query(root, "[data-image-processing-options]").forEach(function (panel) {
      if (bound(panel, "adminImageOptionsBound")) {
        return;
      }
      var mode = panel.querySelector('[name="upload_image_processing"]');
      var quality = panel.querySelector("[data-webp-quality]");
      var thumbnailMode = panel.querySelector('[name="thumbnail_format"]');
      var thumbnailQuality = panel.querySelector("[data-thumbnail-quality]");
      if (!mode || !quality) {
        return;
      }
      function syncQualityVisibility() {
        var customQuality = mode.value === "webp_quality";
        quality.hidden = !customQuality;
        var input = quality.querySelector('[name="upload_webp_quality"]');
        if (input) {
          input.required = customQuality;
        }
      }
      mode.addEventListener("change", syncQualityVisibility);
      syncQualityVisibility();
      if (thumbnailMode && thumbnailQuality) {
        function syncThumbnailQualityVisibility() {
          thumbnailQuality.hidden = thumbnailMode.value === "disabled";
        }
        thumbnailMode.addEventListener("change", syncThumbnailQualityVisibility);
        syncThumbnailQualityVisibility();
      }
    });
  }

  function initAdminNotices(root) {
    if (!document.body.classList.contains("admin-page")) {
      return;
    }
    query(root, ".notice").forEach(function (notice) {
      if (notice.dataset.adminNoticeHandled || !isToastNotice(notice)) {
        return;
      }
      notice.dataset.adminNoticeHandled = "1";
      showMessage(notice.textContent.trim(), { type: notice.classList.contains("danger") ? "error" : "info" });
      notice.remove();
    });
  }

  function isToastNotice(notice) {
    if (!notice || notice.querySelector("form, table, mdui-button, button, a")) {
      return false;
    }
    var text = notice.textContent.trim();
    if (!text || text.length > 140 || text.indexOf("导入预览") !== -1) {
      return false;
    }
    return true;
  }

  function initAdminPjax() {
    if (pjaxReady) {
      return;
    }
    pjaxReady = true;

    if (history.state === null) {
      history.replaceState({ pjax: true, url: window.location.href }, "", window.location.href);
    }

    document.addEventListener("click", function (event) {
      var submitter = submitterElementFromEvent(event);
      if (submitter) {
        lastSubmitter = submitter;
        window.setTimeout(function () {
          if (lastSubmitter === submitter) {
            lastSubmitter = null;
          }
        }, 1000);
      }

      var link = hrefElementFromEvent(event);
      if (!link || !shouldPjaxLink(event, link)) {
        return;
      }
      event.preventDefault();
      pjaxVisit(new URL(link.getAttribute("href"), window.location.href).href);
    }, true);

    document.addEventListener("submit", function (event) {
      var form = event.target;
      var submitter = submitterForForm(form, lastSubmitter) || submitterForForm(form, event.submitter);
      if (!submitter && isSubmitElement(document.activeElement)) {
        submitter = submitterForForm(form, document.activeElement);
      }
      lastSubmitter = null;
      if (!shouldPjaxForm(form, submitter)) {
        return;
      }
      event.preventDefault();
      submitPjaxForm(form, submitter);
    }, true);

    window.addEventListener("popstate", function () {
      if (!sameAdminURL(new URL(window.location.href))) {
        window.location.reload();
        return;
      }
      pjaxVisit(window.location.href, { history: false, skipConfirm: true });
    });
  }

  function shouldPjaxLink(event, element) {
    if (!document.body.classList.contains("admin-page")) {
      return false;
    }
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) {
      return false;
    }
    if (element.closest("[data-no-pjax]") || element.hasAttribute("download")) {
      return false;
    }
    var target = element.getAttribute("target");
    if (target && target !== "_self") {
      return false;
    }
    var href = element.getAttribute("href");
    if (!href || href.charAt(0) === "#") {
      return false;
    }
    var url = new URL(href, window.location.href);
    if (!sameAdminURL(url)) {
      return false;
    }
    if (url.pathname === window.location.pathname && url.search === window.location.search && url.hash) {
      return false;
    }
    return true;
  }

  function shouldPjaxForm(form, submitter) {
    if (!document.body.classList.contains("admin-page") || !form || !form.matches || !form.matches("form")) {
      return false;
    }
    if (form.closest("[data-no-pjax]")) {
      return false;
    }
    var target = attrValue(submitter, "formtarget") || form.getAttribute("target");
    if (target && target !== "_self") {
      return false;
    }
    var method = formMethod(form, submitter);
    if (method === "dialog") {
      return false;
    }
    var url = formActionURL(form, submitter);
    if (!sameAdminURL(url)) {
      return false;
    }
    if (url.pathname === "/admin/logout" || url.pathname === "/admin/backup") {
      return false;
    }
    if (url.pathname === "/admin/autosave" || url.pathname === "/admin/schema/upload" || url.pathname === "/admin/management/upload") {
      return false;
    }
    if (url.pathname.indexOf("/admin/ajax/") === 0 || url.pathname === "/admin/tags/search") {
      return false;
    }
    var enctype = (attrValue(submitter, "formenctype") || form.getAttribute("enctype") || "").toLowerCase();
    if (method !== "get" && enctype === "multipart/form-data") {
      return false;
    }
    return true;
  }

  function submitPjaxForm(form, submitter) {
    adminDirty = false;
    var method = formMethod(form, submitter);
    var url = formActionURL(form, submitter);
    var data = new FormData(form);
    appendSubmitter(data, submitter);
    var params = formParams(data);

    if (method === "get") {
      url.search = params.toString();
      pjaxVisit(url.href);
      return;
    }

    pjaxVisit(url.href, {
      method: method.toUpperCase(),
      body: params,
      submitter: submitter,
      preserveMainScroll: shouldPreserveFormScroll(form, url, submitter)
    });
  }

  function pjaxVisit(url, options) {
    options = options || {};
    if (!options.skipConfirm && !confirmNavigation()) {
      return;
    }

    if (pjaxAbort) {
      pjaxAbort.abort();
    }
    pjaxAbort = new AbortController();
    document.body.classList.add("admin-pjax-loading");
    if (options.preserveMainScroll) {
      options.mainScrollTop = adminMainScrollTop();
    }
    setSubmitterLoading(options.submitter, true);

    fetch(url, {
      method: options.method || "GET",
      body: options.body || null,
      credentials: "same-origin",
      redirect: "follow",
      signal: pjaxAbort.signal,
      headers: {
        "Accept": "text/html, application/xhtml+xml",
        "X-PJAX": "true"
      }
    }).then(function (response) {
      var contentType = response.headers.get("Content-Type") || "";
      if (response.status === 204) {
        return null;
      }
      if (contentType.indexOf("text/html") === -1 && contentType.indexOf("application/xhtml+xml") === -1) {
        throw new Error("服务器没有返回后台页面");
      }
      return response.text().then(function (html) {
        return applyPjaxHTML(html, response.url || url, options);
      });
    }).catch(function (err) {
      if (err.name !== "AbortError") {
        showMessage("加载失败：" + err.message);
      }
    }).finally(function () {
      setSubmitterLoading(options.submitter, false);
      document.body.classList.remove("admin-pjax-loading");
      pjaxAbort = null;
    });
  }

  function applyPjaxHTML(html, finalURL, options) {
    var doc = new DOMParser().parseFromString(html, "text/html");
    var nextMain = doc.querySelector(".admin-main");
    var currentMain = document.querySelector(".admin-main");

    if (!doc.body.classList.contains("admin-page") || !nextMain || !currentMain) {
      window.location.assign(finalURL);
      return false;
    }

    copyShellState(doc);
    replaceChildren(currentMain, nextMain);
    updateDocumentMeta(doc);
    updateHistory(finalURL, options);
    if (options && options.preserveMainScroll) {
      restoreAdminMainScroll(options.mainScrollTop || 0);
    } else {
      scrollAdminMainToTop();
    }
    applyAdminAppearance();
    initAdminChrome();
    ensureCSRF(document);
    initAdminPage(currentMain);
    updateActiveNav(finalURL);
    return true;
  }

  function copyShellState(doc) {
    var nextBody = doc.body;
    var drawer = document.querySelector(".admin-drawer");
    var drawerOpen = drawer && drawer.hasAttribute("open");
    var drawerScrollTop = drawer ? drawer.scrollTop : 0;

    document.body.className = nextBody.className;
    document.body.setAttribute("style", nextBody.getAttribute("style") || "");

    Array.prototype.slice.call(document.body.attributes).forEach(function (attr) {
      if (attr.name.indexOf("data-admin-") === 0) {
        document.body.removeAttribute(attr.name);
      }
    });
    Array.prototype.slice.call(nextBody.attributes).forEach(function (attr) {
      if (attr.name.indexOf("data-admin-") === 0) {
        document.body.setAttribute(attr.name, attr.value);
      }
    });

    document.body.classList.toggle("admin-drawer-open", !!drawerOpen);
    document.body.classList.remove("admin-drawer-modal");

    var currentTitle = document.querySelector(".admin-appbar mdui-top-app-bar-title");
    var nextTitle = doc.querySelector(".admin-appbar mdui-top-app-bar-title");
    if (currentTitle && nextTitle) {
      currentTitle.innerHTML = nextTitle.innerHTML;
    }

    var currentNav = document.querySelector(".admin-nav");
    var nextNav = doc.querySelector(".admin-nav");
    if (currentNav && nextNav) {
      replaceChildren(currentNav, nextNav);
    }
    restoreDrawerScroll(drawerScrollTop);
  }

  function restoreDrawerScroll(scrollTop) {
    var drawer = document.querySelector(".admin-drawer");
    if (!drawer) {
      return;
    }
    drawer.scrollTop = scrollTop;
    window.requestAnimationFrame(function () {
      drawer.scrollTop = scrollTop;
    });
  }

  function updateDocumentMeta(doc) {
    document.title = doc.title;
    var currentCSRF = document.querySelector('meta[name="csrf-token"]');
    var nextCSRF = doc.querySelector('meta[name="csrf-token"]');
    if (currentCSRF && nextCSRF) {
      currentCSRF.setAttribute("content", nextCSRF.getAttribute("content") || "");
    }
  }

  function updateHistory(finalURL, options) {
    if (options && options.history === false) {
      return;
    }
    var state = { pjax: true, url: finalURL };
    if ((options && options.replace) || finalURL === window.location.href) {
      history.replaceState(state, "", finalURL);
    } else {
      history.pushState(state, "", finalURL);
    }
  }

  function updateActiveNav(urlValue) {
    var current = new URL(urlValue || window.location.href, window.location.href);
    query(document, ".admin-nav [href]").forEach(function (item) {
      var url = new URL(item.getAttribute("href"), window.location.href);
      var active = false;
      if (url.pathname === "/admin") {
        active = current.pathname === "/admin" || current.pathname === "/admin/";
      } else {
        active = current.pathname === url.pathname || current.pathname.indexOf(url.pathname + "/") === 0;
      }
      item.toggleAttribute("active", active);
      item.active = active;
    });
  }

  function scrollAdminMainToTop() {
    var scroller = document.querySelector(".admin-layout-main");
    if (scroller && typeof scroller.scrollTo === "function") {
      scroller.scrollTo(0, 0);
    } else if (scroller) {
      scroller.scrollTop = 0;
    } else {
      window.scrollTo(0, 0);
    }
  }

  function adminMainScrollTop() {
    var scroller = document.querySelector(".admin-layout-main");
    return scroller ? scroller.scrollTop : window.scrollY || 0;
  }

  function restoreAdminMainScroll(scrollTop) {
    var scroller = document.querySelector(".admin-layout-main");
    if (scroller) {
      scroller.scrollTop = scrollTop;
      window.requestAnimationFrame(function () {
        scroller.scrollTop = scrollTop;
      });
      return;
    }
    window.scrollTo(0, scrollTop);
  }

  function confirmNavigation() {
    if (!adminDirty) {
      return true;
    }
    return window.confirm("有未保存修改，确定离开？");
  }

  function sameAdminURL(url) {
    if (url.origin !== window.location.origin) {
      return false;
    }
    if (!(url.pathname === "/admin" || url.pathname.indexOf("/admin/") === 0)) {
      return false;
    }
    if (url.pathname.indexOf("/admin/assets/") === 0) {
      return false;
    }
    return true;
  }

  function hrefElementFromEvent(event) {
    var path = typeof event.composedPath === "function" ? event.composedPath() : [];
    for (var i = 0; i < path.length; i += 1) {
      if (path[i] && path[i].nodeType === 1 && path[i].hasAttribute && path[i].hasAttribute("href")) {
        return path[i];
      }
    }
    if (event.target && event.target.closest) {
      return event.target.closest("[href]");
    }
    return null;
  }

  function formActionURL(form, submitter) {
    return new URL(attrValue(submitter, "formaction") || form.getAttribute("action") || window.location.href, window.location.href);
  }

  function formMethod(form, submitter) {
    return (attrValue(submitter, "formmethod") || form.getAttribute("method") || "get").toLowerCase();
  }

  function appendSubmitter(data, submitter) {
    if (!submitter || !submitter.getAttribute) {
      return;
    }
    var name = submitter.getAttribute("name");
    if (!name) {
      return;
    }
    data.append(name, submitter.getAttribute("value") || "");
  }

  function shouldPreserveFormScroll(form, url, submitter) {
    if (!form || !url) {
      return false;
    }
    if (submitter && submitter.hasAttribute && submitter.hasAttribute("data-reset-scroll")) {
      return false;
    }
    if (form.hasAttribute("data-preserve-scroll")) {
      return true;
    }
    var current = new URL(window.location.href);
    return url.pathname === current.pathname && url.search === current.search;
  }

  function formParams(data) {
    var params = new URLSearchParams();
    data.forEach(function (value, key) {
      if (!(typeof File !== "undefined" && value instanceof File)) {
        params.append(key, value);
      }
    });
    return params;
  }

  function submitterElementFromEvent(event) {
    var path = typeof event.composedPath === "function" ? event.composedPath() : [];
    for (var i = 0; i < path.length; i += 1) {
      if (isSubmitElement(path[i])) {
        return path[i];
      }
    }
    return null;
  }

  function submitterForForm(form, submitter) {
    if (!form || !submitter || !isSubmitElement(submitter)) {
      return null;
    }
    if (submitter.form === form || form.contains(submitter)) {
      return submitter;
    }
    var formID = submitter.getAttribute && submitter.getAttribute("form");
    return formID && document.getElementById(formID) === form ? submitter : null;
  }

  function isSubmitElement(element) {
    if (!element || !element.getAttribute) {
      return false;
    }
    var tag = (element.tagName || "").toLowerCase();
    if (tag === "button") {
      return (element.getAttribute("type") || "submit").toLowerCase() === "submit";
    }
    if (tag === "input") {
      var inputType = (element.getAttribute("type") || "text").toLowerCase();
      return inputType === "submit" || inputType === "image";
    }
    if (tag === "mdui-button" || tag === "mdui-button-icon") {
      return (element.getAttribute("type") || "").toLowerCase() === "submit";
    }
    return false;
  }

  function attrValue(element, name) {
    if (!element || !element.getAttribute) {
      return "";
    }
    return element.getAttribute(name) || "";
  }

  function setSubmitterLoading(submitter, loading) {
    if (!submitter || typeof submitter !== "object" || !("loading" in submitter)) {
      return;
    }
    submitter.loading = loading;
  }

  function setButtonLabel(button, text) {
    var label = button && button.querySelector && button.querySelector(".button-label");
    if (label) {
      label.textContent = text;
      return;
    }
    if (button) {
      button.textContent = text;
    }
  }

  function replaceChildren(target, source) {
    while (target.firstChild) {
      target.removeChild(target.firstChild);
    }
    Array.prototype.slice.call(source.childNodes).forEach(function (node) {
      target.appendChild(document.importNode(node, true));
    });
  }

  function bound(element, key) {
    if (element.dataset[key]) {
      return true;
    }
    element.dataset[key] = "1";
    return false;
  }

  function query(root, selector) {
    return Array.prototype.slice.call((root || document).querySelectorAll(selector));
  }

  function csrfToken() {
    var csrf = document.querySelector('meta[name="csrf-token"]');
    return csrf && csrf.content ? csrf.content : "";
  }

  function showMessage(message, options) {
    options = options || {};
    var type = options.type || "error";
    if (window.mdui && typeof window.mdui.snackbar === "function") {
      var snackbar = window.mdui.snackbar({
        message: escapeHTML(message),
        className: "admin-snackbar admin-snackbar-" + type,
        closeable: true,
        messageLine: message.length > 44 ? 2 : 1,
        autoCloseDelay: type === "error" ? 6000 : 3600,
        queue: "admin-notices"
      });
      applySnackbarTheme(snackbar, type);
      return;
    }
    window.alert(message);
  }

  function applySnackbarTheme(snackbar, type) {
    if (!snackbar || !snackbar.style) {
      return;
    }
    var error = type === "error";
    snackbar.classList.add("admin-snackbar", error ? "admin-snackbar-error" : "admin-snackbar-info");
    snackbar.style.setProperty("color", "rgb(var(--mdui-color-on-" + (error ? "error" : "primary") + "-container))");
    snackbar.style.setProperty("background", "rgba(var(--mdui-color-" + (error ? "error" : "primary") + "-container), var(--admin-topbar-opacity))");
    snackbar.style.setProperty("border", "1px solid rgba(var(--mdui-color-" + (error ? "error" : "primary") + "), " + (error ? "0.3" : "0.22") + ")");
    snackbar.style.setProperty("border-radius", "8px");
    snackbar.style.setProperty("box-shadow", "0 6px 20px rgba(var(--mdui-color-shadow), 0.16)");
  }

  function escapeHTML(value) {
    return String(value).replace(/[&<>"']/g, function (char) {
      return {
        "&": "&amp;",
        "<": "&lt;",
        ">": "&gt;",
        '"': "&quot;",
        "'": "&#39;"
      }[char];
    });
  }

  function cssURL(value) {
    return 'url("' + String(value).replace(/["\\\n\r\f]/g, "\\$&") + '")';
  }

  function cssEscape(value) {
    if (window.CSS && typeof window.CSS.escape === "function") {
      return window.CSS.escape(value);
    }
    return String(value).replace(/["\\]/g, "\\$&");
  }
})();
