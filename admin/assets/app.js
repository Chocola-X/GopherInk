(function () {
  var adminDirty = false;
  var pjaxAbort = null;
  var pjaxReady = false;
  var lastSubmitter = null;

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
    initCopyButtons(root);
    initSchemaForm(root);
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
          cidInput.value = String(id);
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
        wrapper.innerHTML = '<mdui-text-field name="field_name" label="字段名"></mdui-text-field><select name="field_type"><option value="str">字符串</option><option value="int">整数</option><option value="float">浮点数</option><option value="json">JSON</option></select><mdui-text-field name="field_value" label="值"></mdui-text-field><mdui-button-icon type="button" class="remove-field" title="删除字段"><mdui-icon name="delete"></mdui-icon></mdui-button-icon>';
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
    query(root, ".media-pick").forEach(function (button) {
      if (bound(button, "adminMediaPickBound")) {
        return;
      }
      button.addEventListener("click", function () {
        appendToEditor(button.dataset.markdown || "");
      });
    });
  }

  function appendToEditor(text) {
    var editor = document.querySelector("#content-text");
    if (!editor || !text) {
      return;
    }
    var current = editor.value || "";
    var prefix = current && !/\n$/.test(current) ? "\n\n" : "";
    editor.value = current + prefix + text + "\n";
    editor.dispatchEvent(new Event("input", { bubbles: true }));
    editor.focus();
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

      function selectedFile() {
        if (fileField.files && fileField.files.length) {
          return fileField.files[0];
        }
        var input = fileField.querySelector && fileField.querySelector('input[type="file"]');
        if (input && input.files && input.files.length) {
          return input.files[0];
        }
        if (fileField.shadowRoot) {
          input = fileField.shadowRoot.querySelector('input[type="file"]');
          if (input && input.files && input.files.length) {
            return input.files[0];
          }
        }
        return null;
      }

      button.addEventListener("click", function () {
        var csrf = csrfToken();
        var file = selectedFile();
        if (!csrf) {
          button.textContent = "令牌失效";
          return;
        }
        if (!file) {
          button.textContent = "请选择文件";
          return;
        }
        var data = new FormData();
        data.set("_csrf", csrf);
        data.set("file", file);
        if (cid && cid.value) {
          data.set("cid", cid.value);
        }
        fetch("/admin/medias", {
          method: "POST",
          body: data,
          headers: { "Accept": "application/json" },
          credentials: "same-origin"
        }).then(function (res) {
          if (!res.ok) {
            throw new Error("upload failed");
          }
          return res.json();
        }).then(function (payload) {
          appendToEditor(payload.markdown || payload.url || "");
          button.textContent = "已插入";
        }).catch(function () {
          button.textContent = "上传失败";
        });
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
        if (!value || !navigator.clipboard) {
          return;
        }
        navigator.clipboard.writeText(value).then(function () {
          button.textContent = "已复制";
        }).catch(function () {});
      });
    });
  }

  function initSchemaForm(root) {
    query(root, "form[data-schema-upload-url]").forEach(function (form) {
      if (bound(form, "adminSchemaFormBound")) {
        return;
      }
      var uploadURL = form.dataset.schemaUploadUrl || "/admin/schema/upload";

      query(form, ".schema-swatch").forEach(function (button) {
        button.addEventListener("click", function () {
          var wrap = button.closest(".schema-swatches");
          var target = wrap && form.querySelector('[name="' + cssEscape(wrap.dataset.target || "") + '"]');
          if (target) {
            target.value = button.dataset.color || "";
            target.dispatchEvent(new Event("input", { bubbles: true }));
          }
        });
      });

      query(form, ".schema-pick-file").forEach(function (button) {
        button.addEventListener("click", function () {
          var field = button.closest(".schema-image-field");
          var fileInput = field && field.querySelector(".schema-native-file");
          if (fileInput) {
            fileInput.click();
          }
        });
      });

      query(form, ".schema-native-file").forEach(function (input) {
        input.addEventListener("change", function () {
          var field = input.closest(".schema-image-field");
          var label = field && field.querySelector(".schema-file-name");
          if (label) {
            label.textContent = input.files && input.files[0] ? input.files[0].name : "未选择文件";
          }
        });
      });

      query(form, ".schema-upload-button").forEach(function (button) {
        button.addEventListener("click", function () {
          var field = button.closest(".schema-image-field");
          var fileInput = field && field.querySelector(".schema-native-file");
          if (!fileInput || !fileInput.files || fileInput.files.length === 0) {
            return;
          }
          var data = new FormData();
          data.append("file", fileInput.files[0]);
          var csrf = csrfToken();
          if (csrf) {
            data.append("_csrf", csrf);
          }
          button.loading = true;
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
            })
            .catch(function (err) {
              showMessage("上传失败：" + err.message);
            })
            .finally(function () {
              button.loading = false;
            });
        });
      });
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
