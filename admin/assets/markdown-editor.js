(function () {
  "use strict";

  function init(root) {
    query(root || document, "[data-markdown-editor]").forEach(function (wrap) {
      if (wrap.dataset.markdownEditorBound) {
        return;
      }
      wrap.dataset.markdownEditorBound = "1";
      bindEditor(wrap);
    });
  }

  function bindEditor(wrap) {
    var textarea = wrap.querySelector(".markdown-source");
    var highlight = wrap.querySelector(".markdown-highlight");
    var shell = wrap.querySelector(".markdown-editor-shell");
    var preview = wrap.querySelector(".markdown-preview");
    var previewToggle = wrap.querySelector(".markdown-preview-toggle");
    if (!textarea || !highlight || !shell || !preview) {
      return;
    }

    shell.classList.add("markdown-highlight-enabled");
    syncHighlight(textarea, highlight);

    textarea.addEventListener("input", function () {
      syncHighlight(textarea, highlight);
    });
    textarea.addEventListener("scroll", function () {
      highlight.scrollTop = textarea.scrollTop;
      highlight.scrollLeft = textarea.scrollLeft;
    });

    query(wrap, "[data-md-action]").forEach(function (button) {
      button.addEventListener("click", function () {
        setEditMode(wrap, textarea, preview, previewToggle);
        applyAction(textarea, button.dataset.mdAction);
        syncHighlight(textarea, highlight);
      });
    });

    if (previewToggle) {
      previewToggle.addEventListener("click", function () {
        if (wrap.dataset.markdownMode === "preview") {
          setEditMode(wrap, textarea, preview, previewToggle);
          return;
        }
        renderPreview(wrap, textarea, preview, previewToggle);
      });
    }
  }

  function applyAction(textarea, action) {
    var value = textarea.value || "";
    var start = textarea.selectionStart || 0;
    var end = textarea.selectionEnd || start;
    var selected = value.slice(start, end);
    var next = null;
    var range = null;

    switch (action) {
    case "heading":
      range = lineRange(value, start, end);
      next = replaceRange(value, range.start, range.end, prefixLines(value.slice(range.start, range.end) || "标题", "## "));
      setValue(textarea, next.value, next.start, next.end);
      return;
    case "quote":
      range = lineRange(value, start, end);
      next = replaceRange(value, range.start, range.end, prefixLines(value.slice(range.start, range.end) || "引用内容", "> "));
      setValue(textarea, next.value, next.start, next.end);
      return;
    case "unordered-list":
      range = lineRange(value, start, end);
      next = replaceRange(value, range.start, range.end, prefixLines(value.slice(range.start, range.end) || "列表项", "- "));
      setValue(textarea, next.value, next.start, next.end);
      return;
    case "ordered-list":
      range = lineRange(value, start, end);
      next = replaceRange(value, range.start, range.end, numberLines(value.slice(range.start, range.end) || "列表项"));
      setValue(textarea, next.value, next.start, next.end);
      return;
    case "bold":
      wrapSelection(textarea, "**", "**", "粗体文字");
      return;
    case "italic":
      wrapSelection(textarea, "*", "*", "斜体文字");
      return;
    case "inline-code":
      wrapSelection(textarea, "`", "`", "code");
      return;
    case "code-block":
      wrapSelection(textarea, "```\n", "\n```", selected || "code");
      return;
    case "link":
      insertLink(textarea, false);
      return;
    case "image":
      insertLink(textarea, true);
      return;
    case "hr":
      insertBlock(textarea, "\n\n---\n\n");
      return;
    case "more":
      insertBlock(textarea, "\n\n<!--more-->\n\n");
      return;
    default:
      textarea.focus();
    }
  }

  function wrapSelection(textarea, before, after, fallback) {
    var value = textarea.value || "";
    var start = textarea.selectionStart || 0;
    var end = textarea.selectionEnd || start;
    var selected = value.slice(start, end) || fallback;
    var replacement = before + selected + after;
    textarea.value = value.slice(0, start) + replacement + value.slice(end);
    textarea.focus();
    textarea.setSelectionRange(start + before.length, start + before.length + selected.length);
    dispatchInput(textarea);
  }

  function insertLink(textarea, image) {
    var value = textarea.value || "";
    var start = textarea.selectionStart || 0;
    var end = textarea.selectionEnd || start;
    var selected = value.slice(start, end) || (image ? "图片描述" : "链接文字");
    openLinkDialog({
      image: image,
      text: selected,
      url: "",
      onConfirm: function (text, url) {
        var label = text.trim() || (image ? "图片描述" : "链接文字");
        var href = url.trim() || "https://example.com";
        var replacement = (image ? "!" : "") + "[" + escapeMarkdownLabel(label) + "](" + href + ")";
        textarea.value = value.slice(0, start) + replacement + value.slice(end);
        textarea.focus();
        textarea.setSelectionRange(start + replacement.length, start + replacement.length);
        dispatchInput(textarea);
      },
      onCancel: function () {
        textarea.focus();
      }
    });
  }

  function openLinkDialog(options) {
    var dialog = document.createElement("mdui-dialog");
    var title = options.image ? "插入图片" : "插入链接";
    dialog.setAttribute("headline", title);
    dialog.className = "markdown-link-dialog";
    dialog.innerHTML = [
      '<div class="markdown-link-dialog-body">',
      '<mdui-text-field class="markdown-link-text" label="描述文本" clearable></mdui-text-field>',
      '<mdui-text-field class="markdown-link-url" label="链接" clearable></mdui-text-field>',
      '</div>',
      '<mdui-button slot="action" variant="text" class="markdown-link-cancel">取消</mdui-button>',
      '<mdui-button slot="action" class="markdown-link-confirm">插入</mdui-button>'
    ].join("");
    document.body.appendChild(dialog);

    var textField = dialog.querySelector(".markdown-link-text");
    var urlField = dialog.querySelector(".markdown-link-url");
    var cancel = dialog.querySelector(".markdown-link-cancel");
    var confirm = dialog.querySelector(".markdown-link-confirm");
    textField.value = options.text || "";
    urlField.value = options.url || "";

    function closeDialog() {
      dialog.open = false;
    }

    function submitDialog() {
      var text = textField.value || "";
      var url = urlField.value || "";
      options.onConfirm(text, url);
      closeDialog();
    }

    cancel.addEventListener("click", function () {
      if (options.onCancel) {
        options.onCancel();
      }
      closeDialog();
    });
    confirm.addEventListener("click", submitDialog);
    dialog.addEventListener("keydown", function (event) {
      if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) {
        event.preventDefault();
        submitDialog();
      }
    });
    dialog.addEventListener("closed", function () {
      dialog.remove();
    });

    requestAnimationFrame(function () {
      dialog.open = true;
      textField.focus();
    });
  }

  function escapeMarkdownLabel(value) {
    return value.replace(/([\\\]])/g, "\\$1");
  }

  function insertBlock(textarea, text) {
    var value = textarea.value || "";
    var start = textarea.selectionStart || value.length;
    textarea.value = value.slice(0, start) + text + value.slice(start);
    textarea.focus();
    textarea.setSelectionRange(start + text.length, start + text.length);
    dispatchInput(textarea);
  }

  function renderPreview(wrap, textarea, preview, button) {
    var form = textarea.closest("form");
    var data = new FormData();
    data.set("text", textarea.value || "");
    if (form) {
      var title = form.querySelector('[name="title"]');
      var type = form.querySelector('[name="type"]');
      var csrf = form.querySelector('[name="_csrf"]');
      if (title) {
        data.set("title", title.value || "");
      }
      if (type) {
        data.set("type", type.value || "post");
      }
      if (csrf) {
        data.set("_csrf", csrf.value || "");
      }
    }
    setButtonLabel(button, "渲染中");
    button.loading = true;
    fetch(wrap.dataset.previewUrl || "/admin/markdown/preview", {
      method: "POST",
      body: data,
      headers: { "Accept": "application/json" },
      credentials: "same-origin"
    }).then(function (response) {
      if (!response.ok) {
        throw new Error("preview failed");
      }
      return response.json();
    }).then(function (payload) {
      preview.innerHTML = payload.html || "";
      wrap.dataset.markdownMode = "preview";
      preview.hidden = false;
      textarea.closest(".markdown-editor-shell").hidden = true;
      setButtonLabel(button, "返回编辑");
      setButtonIcon(button, "edit");
    }).catch(function () {
      preview.innerHTML = '<p class="danger">预览生成失败。</p>';
      preview.hidden = false;
    }).finally(function () {
      button.loading = false;
    });
  }

  function setEditMode(wrap, textarea, preview, button) {
    wrap.dataset.markdownMode = "edit";
    preview.hidden = true;
    textarea.closest(".markdown-editor-shell").hidden = false;
    setButtonLabel(button, "预览模式");
    setButtonIcon(button, "visibility");
    textarea.focus();
  }

  function syncHighlight(textarea, highlight) {
    highlight.innerHTML = highlightMarkdown(textarea.value || "") + "\n";
    highlight.scrollTop = textarea.scrollTop;
    highlight.scrollLeft = textarea.scrollLeft;
  }

  function highlightMarkdown(input) {
    var inFence = false;
    return input.replace(/\r\n/g, "\n").split("\n").map(function (line) {
      var escaped = escapeHTML(line);
      if (/^\s*```/.test(line)) {
        inFence = !inFence;
        return '<span class="md-code">' + escaped + '</span>';
      }
      if (inFence) {
        return '<span class="md-code">' + escaped + '</span>';
      }
      if (/^#{1,6}\s/.test(line)) {
        return '<span class="md-heading">' + inlineHighlight(escaped) + '</span>';
      }
      if (/^\s*&gt;\s?/.test(escaped)) {
        return '<span class="md-quote">' + inlineHighlight(escaped) + '</span>';
      }
      if (/^\s*([-*+]|\d+\.)\s+/.test(line)) {
        return '<span class="md-list">' + inlineHighlight(escaped) + '</span>';
      }
      if (/^\s*---+\s*$/.test(line)) {
        return '<span class="md-rule">' + escaped + '</span>';
      }
      return inlineHighlight(escaped);
    }).join("\n");
  }

  function inlineHighlight(value) {
    return value
      .replace(/(`[^`]+`)/g, '<span class="md-inline-code">$1</span>')
      .replace(/(!?\[[^\]]+\]\([^)]+\))/g, '<span class="md-link">$1</span>')
      .replace(/(\*\*|__)(.+?)\1/g, '<span class="md-strong">$1$2$1</span>')
      .replace(/(^|[^\*])(\*|_)([^*_]+)\2/g, '$1<span class="md-em">$2$3$2</span>');
  }

  function lineRange(value, start, end) {
    var lineStart = value.lastIndexOf("\n", Math.max(0, start - 1)) + 1;
    var lineEnd = value.indexOf("\n", end);
    if (lineEnd < 0) {
      lineEnd = value.length;
    }
    return { start: lineStart, end: lineEnd };
  }

  function prefixLines(value, prefix) {
    return value.split("\n").map(function (line) {
      return line ? prefix + line : prefix.trim();
    }).join("\n");
  }

  function numberLines(value) {
    return value.split("\n").map(function (line, index) {
      return (index + 1) + ". " + (line || "列表项");
    }).join("\n");
  }

  function replaceRange(value, start, end, replacement) {
    return {
      value: value.slice(0, start) + replacement + value.slice(end),
      start: start,
      end: start + replacement.length
    };
  }

  function setValue(textarea, value, start, end) {
    textarea.value = value;
    textarea.focus();
    textarea.setSelectionRange(start, end);
    dispatchInput(textarea);
  }

  function dispatchInput(textarea) {
    textarea.dispatchEvent(new Event("input", { bubbles: true }));
  }

  function setButtonLabel(button, label) {
    if (!button) {
      return;
    }
    var text = button.querySelector(".button-label");
    if (text) {
      text.textContent = label;
    }
  }

  function setButtonIcon(button, icon) {
    if (!button) {
      return;
    }
    var element = button.querySelector("mdui-icon");
    if (element) {
      element.setAttribute("name", icon);
    }
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

  function query(root, selector) {
    return Array.prototype.slice.call((root || document).querySelectorAll(selector));
  }

  window.GoBlogMarkdownEditor = { init: init };
})();
