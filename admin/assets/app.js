(function () {
  function ready(fn) {
    if (document.readyState === "loading") {
      document.addEventListener("DOMContentLoaded", fn);
    } else {
      fn();
    }
  }

  ready(function () {
    var csrf = document.querySelector('meta[name="csrf-token"]');
    if (csrf && csrf.content) {
      document.querySelectorAll('form[method="post"], form[method="POST"]').forEach(function (form) {
        if (!form.querySelector('input[name="_csrf"]')) {
          var input = document.createElement("input");
          input.type = "hidden";
          input.name = "_csrf";
          input.value = csrf.content;
          form.appendChild(input);
        }
      });
    }

    document.querySelectorAll(".field-stack").forEach(function (form) {
      if (!form.matches('form[method="post"], form[method="POST"]')) {
        return;
      }

      var cidInput = form.querySelector('input[name="cid"]');
      var typeInput = form.querySelector('input[name="type"]');
      var status = form.querySelector(".autosave-status");
      var dirty = false;
      var timer = null;

      function markClean() {
        dirty = false;
      }

      function scheduleAutosave() {
        if (!cidInput || !typeInput || !csrf || !csrf.content) {
          return;
        }
        dirty = true;
        if (status) {
          status.textContent = "有未保存修改";
        }
        clearTimeout(timer);
        timer = setTimeout(function () {
          var data = new FormData(form);
          data.set("_csrf", csrf.content);
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
            if (payload.cid && !cidInput.value) {
              cidInput.value = payload.cid;
            }
            markClean();
            if (status) {
              status.textContent = "已自动保存";
            }
          }).catch(function () {
            if (status) {
              status.textContent = "自动保存失败";
            }
          });
        }, 3000);
      }

      form.addEventListener("input", scheduleAutosave);
      form.addEventListener("change", scheduleAutosave);
      form.addEventListener("submit", markClean);
      window.addEventListener("beforeunload", function (event) {
        if (!dirty) {
          return;
        }
        event.preventDefault();
        event.returnValue = "";
      });
    });

    document.querySelectorAll(".custom-fields").forEach(function (fieldset) {
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

    document.querySelectorAll(".tag-input").forEach(function (input) {
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

    document.querySelectorAll(".media-pick").forEach(function (button) {
      button.addEventListener("click", function () {
        appendToEditor(button.dataset.markdown || "");
      });
    });

    document.querySelectorAll(".editor-upload").forEach(function (panel) {
      var fileField = panel.querySelector(".editor-upload-file");
      var button = panel.querySelector(".editor-upload-button");
      var cid = document.querySelector('input[name="cid"]');
      if (!fileField || !button || !csrf || !csrf.content) {
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
        var file = selectedFile();
        if (!file) {
          button.textContent = "请选择文件";
          return;
        }
        var data = new FormData();
        data.set("_csrf", csrf.content);
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

    document.querySelectorAll(".copy-text").forEach(function (button) {
      button.addEventListener("click", function () {
        var value = button.dataset.copy || "";
        if (!value) {
          return;
        }
        if (navigator.clipboard) {
          navigator.clipboard.writeText(value).then(function () {
            button.textContent = "已复制";
          }).catch(function () {});
        }
      });
    });

    var drawer = document.querySelector(".admin-drawer");
    var toggle = document.querySelector(".drawer-toggle");
    var scrim = document.querySelector(".drawer-scrim");

    if (!drawer || !toggle || !scrim) {
      return;
    }

    function setDrawer(open, modal) {
      drawer.open = open;
      if (open) {
        drawer.setAttribute("open", "");
      } else {
        drawer.removeAttribute("open");
      }
      document.body.classList.toggle("admin-drawer-open", open);
      document.body.classList.toggle("admin-drawer-modal", open && !!modal);
      localStorage.setItem("goblogAdminDrawerOpen", open ? "1" : "0");
    }

    var stored = localStorage.getItem("goblogAdminDrawerOpen");
    setDrawer(stored === null ? window.matchMedia("(min-width: 920px)").matches : stored === "1", false);

    toggle.addEventListener("click", function () {
      setDrawer(!drawer.open, !drawer.open);
    });

    scrim.addEventListener("click", function () {
      setDrawer(false, false);
    });

    window.addEventListener("keydown", function (event) {
      if (event.key === "Escape" && drawer.open) {
        setDrawer(false, false);
      }
    });
  });
})();
