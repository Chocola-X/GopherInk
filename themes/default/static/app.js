(function () {
  const body = document.body;
  const progress = document.querySelector(".pjax-progress");
  const color = body.dataset.primary || "#ff4081";
  let infiniteObserver = null;
  let pageAppendPending = false;

  function setAccent() {
    document.documentElement.style.setProperty("--cuckoo-accent", color);
    if (window.mdui && typeof window.mdui.setColorScheme === "function") {
      try {
        window.mdui.setColorScheme(color);
      } catch (err) {
        window.mdui.setColorScheme("#ff4081");
      }
    }
  }

  function preferDark() {
    return window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches;
  }

  function applyTheme(mode) {
    const configured = body.dataset.themeMode || "auto";
    const stored = localStorage.getItem("cuckoo-brightness");
    const next = mode || stored || configured;
    const dark = next === "dark" || (next === "auto" && preferDark());
    body.classList.toggle("mdui-theme-layout-dark", dark);
    document.documentElement.classList.toggle("mdui-theme-dark", dark);
    document.documentElement.classList.toggle("mdui-theme-light", !dark);
    document.querySelector(".brightness")?.replaceChildren(icon(dark ? "brightness-2" : "brightness-5"));
  }

  function icon(name) {
    const el = document.createElement("mdui-icon");
    el.setAttribute("name", name.replace(/-/g, "_"));
    return el;
  }

  function openDrawer() {
    body.classList.add("drawer-open");
  }

  function closeDrawer() {
    body.classList.remove("drawer-open");
  }

  function refreshBackTop() {
    document.querySelector(".top")?.classList.toggle("is-visible", window.scrollY > 20);
  }

  function slugify(text, index, seen) {
    const base = (text || "")
      .trim()
      .toLowerCase()
      .replace(/[\s/]+/g, "-")
      .replace(/[^\w\u4e00-\u9fa5-]/g, "") || "heading-" + index;
    const used = seen.get(base) || 0;
    seen.set(base, used + 1);
    return used ? base + "-" + used : base;
  }

  function initTOC() {
    const toc = document.querySelector("#toc .toc");
    const content = document.querySelector(".post-content");
    const card = document.querySelector("#toc");
    if (!toc || !content || !card) return;
    const headings = Array.from(content.querySelectorAll("h1, h2, h3, h4"));
    if (!headings.length) {
      card.remove();
      return;
    }
    const seen = new Map();
    headings.forEach((heading, index) => {
      if (!heading.id) heading.id = slugify(heading.textContent, index, seen);
    });
    if (window.tocbot) {
      tocbot.init({
        tocSelector: "#toc .toc",
        contentSelector: ".post-content",
        headingSelector: "h1, h2, h3, h4",
        scrollSmooth: true,
        scrollSmoothOffset: -70,
        headingsOffset: -70,
        collapseDepth: 0,
        orderedList: true,
      });
    }
  }

  function destroyTOC() {
    if (window.tocbot) {
      try { tocbot.destroy(); } catch (e) { /* ignore */ }
    }
  }

  function refreshSearch() {
    document.querySelectorAll(".appbar-search").forEach((form) => {
      const input = form.querySelector("input");
      form.classList.toggle("is-expanded", Boolean(input?.value));
    });
  }

  function initCommentDraft() {
    const form = document.querySelector("#comment-form");
    const field = form?.querySelector('[name="text"]');
    const cid = form?.querySelector('[name="cid"]')?.value;
    if (!form || !field || !cid || form.dataset.commentDraftBound) return;
    form.dataset.commentDraftBound = "1";
    const key = "gopherink-comment-draft:" + cid;
    try {
      const params = new URLSearchParams(window.location.search);
      if (params.get("comment_ok") === "1") {
        localStorage.removeItem(key);
      } else if (!field.value) {
        field.value = localStorage.getItem(key) || "";
      }
      field.addEventListener("input", () => {
        if (field.value) localStorage.setItem(key, field.value);
        else localStorage.removeItem(key);
      });
    } catch (err) {
      // Storage may be unavailable in private browsing or restricted contexts.
    }
  }

  function showCommentSubmitError(form, message) {
    const comments = form.closest("#comments");
    if (!comments) return;
    let notice = comments.querySelector(".comment-submit-error");
    if (!notice) {
      notice = document.createElement("div");
      notice.className = "notice-card notice-error comment-submit-error";
      comments.insertBefore(notice, form);
    }
    notice.textContent = message;
  }

  function initCommentSubmit() {
    const form = document.querySelector("#comment-form");
    if (!form || form.dataset.commentSubmitBound) return;
    form.dataset.commentSubmitBound = "1";
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      if (form.dataset.commentSubmitting === "1") return;
      if (typeof form.reportValidity === "function" && !form.reportValidity()) return;

      const submit = form.querySelector('[type="submit"]');
      form.dataset.commentSubmitting = "1";
      if (submit) submit.disabled = true;
      form.closest("#comments")?.querySelector(".comment-submit-error")?.remove();
      try {
        const guard = form.querySelector('[name="_comment_guard"]');
        if (guard) {
          const endpoint = form.dataset.commentGuardEndpoint;
          const cid = form.querySelector('[name="cid"]')?.value || "";
          if (!endpoint || !cid) throw new Error("评论校验信息不完整，请刷新页面后重试。");
          const guardURL = new URL(endpoint, window.location.href);
          guardURL.searchParams.set("cid", cid);
          guardURL.searchParams.set("_", `${Date.now()}-${Math.random().toString(36).slice(2)}`);
          const guardResponse = await fetch(guardURL, {
            cache: "no-store",
            credentials: "same-origin",
            headers: {
              Accept: "application/json",
              "X-Requested-With": "XMLHttpRequest",
              "X-GopherInk-Comment": "guard",
            },
          });
          if (!guardResponse.ok) throw new Error("评论校验失败，请刷新页面后重试。");
          const payload = await guardResponse.json();
          if (!payload?.token) throw new Error("评论校验信息无效，请刷新页面后重试。");
          guard.value = payload.token;
        }

        const response = await fetch(form.action, {
          method: "POST",
          body: new FormData(form),
          cache: "no-store",
          credentials: "same-origin",
          redirect: "follow",
          headers: {
            "X-Requested-With": "XMLHttpRequest",
            "X-GopherInk-Comment": "submit",
          },
        });
        if (!response.ok) throw new Error(response.status === 403 ? "评论校验已失效，请重新提交。" : "评论提交失败，请稍后重试。");
        window.location.assign(response.url || window.location.href);
      } catch (error) {
        showCommentSubmitError(form, error instanceof Error ? error.message : "评论提交失败，请稍后重试。");
      } finally {
        form.dataset.commentSubmitting = "0";
        if (submit) submit.disabled = false;
      }
    });
  }

  function commentReplyElements() {
    const form = document.querySelector("#comment-form");
    if (!form) return {};
    let placeholder = document.querySelector("#comment-form-placeholder");
    if (!placeholder) {
      placeholder = document.createElement("div");
      placeholder.id = "comment-form-placeholder";
      placeholder.hidden = true;
      form.parentNode.insertBefore(placeholder, form);
    }
    return {
      form,
      placeholder,
      parent: form.querySelector('[name="parent"]'),
      title: form.querySelector(".comment-respond-title"),
      cancel: form.querySelector(".comment-cancel-reply"),
      text: form.querySelector('[name="text"]'),
    };
  }

  function md5Hex(value) {
    const bytes = new TextEncoder().encode(value);
    const paddedLength = (bytes.length + 9 + 63) & ~63;
    const buffer = new Uint8Array(paddedLength);
    buffer.set(bytes);
    buffer[bytes.length] = 0x80;
    const bitLength = BigInt(bytes.length) * 8n;
    for (let index = 0; index < 8; index += 1) {
      buffer[paddedLength - 8 + index] = Number((bitLength >> BigInt(index * 8)) & 0xffn);
    }
    const shifts = [
      7, 12, 17, 22, 7, 12, 17, 22, 7, 12, 17, 22, 7, 12, 17, 22,
      5, 9, 14, 20, 5, 9, 14, 20, 5, 9, 14, 20, 5, 9, 14, 20,
      4, 11, 16, 23, 4, 11, 16, 23, 4, 11, 16, 23, 4, 11, 16, 23,
      6, 10, 15, 21, 6, 10, 15, 21, 6, 10, 15, 21, 6, 10, 15, 21,
    ];
    const constants = Array.from({ length: 64 }, (_, index) => Math.floor(Math.abs(Math.sin(index + 1)) * 0x100000000));
    const rotate = (input, amount) => (input << amount) | (input >>> (32 - amount));
    let a0 = 0x67452301;
    let b0 = 0xefcdab89;
    let c0 = 0x98badcfe;
    let d0 = 0x10325476;
    const view = new DataView(buffer.buffer);

    for (let offset = 0; offset < buffer.length; offset += 64) {
      const words = Array.from({ length: 16 }, (_, index) => view.getUint32(offset + index * 4, true));
      let a = a0;
      let b = b0;
      let c = c0;
      let d = d0;
      for (let index = 0; index < 64; index += 1) {
        let mixed;
        let word;
        if (index < 16) {
          mixed = (b & c) | (~b & d);
          word = index;
        } else if (index < 32) {
          mixed = (d & b) | (~d & c);
          word = (5 * index + 1) % 16;
        } else if (index < 48) {
          mixed = b ^ c ^ d;
          word = (3 * index + 5) % 16;
        } else {
          mixed = c ^ (b | ~d);
          word = (7 * index) % 16;
        }
        const previousD = d;
        d = c;
        c = b;
        b = (b + rotate((a + mixed + constants[index] + words[word]) | 0, shifts[index])) | 0;
        a = previousD;
      }
      a0 = (a0 + a) | 0;
      b0 = (b0 + b) | 0;
      c0 = (c0 + c) | 0;
      d0 = (d0 + d) | 0;
    }

    return [a0, b0, c0, d0].map((word) => {
      let output = "";
      for (let index = 0; index < 4; index += 1) {
        output += ((word >>> (index * 8)) & 0xff).toString(16).padStart(2, "0");
      }
      return output;
    }).join("");
  }

  function initCommentAvatarPreview() {
    const form = document.querySelector("#comment-form");
    const preview = form?.querySelector("[data-comment-avatar-preview]");
    const mail = form?.querySelector('[name="mail"]');
    if (!form || !preview || !mail || preview.dataset.avatarBound) return;
    preview.dataset.avatarBound = "1";
    let timer;
    const update = () => {
      const value = String(mail.value || "").trim().toLowerCase();
      if (!value || !value.includes("@")) {
        preview.src = preview.dataset.avatarDefault || "";
        return;
      }
      preview.src = (preview.dataset.avatarTemplate || "").replaceAll("{hash}", md5Hex(value));
    };
    const schedule = () => {
      window.clearTimeout(timer);
      timer = window.setTimeout(update, 350);
    };
    mail.addEventListener("input", schedule);
    mail.addEventListener("change", update);
    update();
  }

  function focusCommentField(field) {
    if (!field) return;
    const focus = () => field.focus();
    if (field.updateComplete?.then) field.updateComplete.then(focus);
    else window.setTimeout(focus, 0);
  }

  function moveCommentForm(parentID, author, focus = true) {
    const parts = commentReplyElements();
    const target = document.getElementById(`comment-${String(parentID || "")}`);
    if (!parts.form || !parts.parent || !target) return false;
    const slot = Array.from(target.children).find((child) => child.matches("[data-comment-reply-slot]"));
    if (!slot) return false;
    parts.parent.value = String(parentID);
    parts.form.classList.add("is-replying");
    if (parts.title) parts.title.textContent = author ? `回复 @${author}` : "回复评论";
    if (parts.cancel) parts.cancel.hidden = false;
    slot.appendChild(parts.form);
    if (focus) {
      parts.form.scrollIntoView({ behavior: "smooth", block: "center" });
      focusCommentField(parts.text);
    }
    return true;
  }

  function cancelCommentReply() {
    const parts = commentReplyElements();
    if (!parts.form || !parts.placeholder?.parentNode) return;
    parts.parent.value = "0";
    parts.form.classList.remove("is-replying");
    if (parts.title) parts.title.textContent = "发表评论";
    if (parts.cancel) parts.cancel.hidden = true;
    parts.placeholder.parentNode.insertBefore(parts.form, parts.placeholder.nextSibling);
    const url = new URL(window.location.href);
    if (url.searchParams.has("reply")) {
      url.searchParams.delete("reply");
      history.replaceState(history.state, "", url.pathname + url.search + "#comments");
    }
  }

  function initCommentReply() {
    const parts = commentReplyElements();
    const parentID = parts.parent?.value || "";
    if (!parts.form || !parentID || parentID === "0") return;
    const target = document.getElementById(`comment-${parentID}`);
    if (moveCommentForm(parentID, target?.dataset.commentAuthor || "", false)) {
      window.requestAnimationFrame(() => parts.form.scrollIntoView({ block: "center" }));
    }
  }

  function closeSearch(form, clear = false) {
    if (!form) return;
    const input = form.querySelector("input");
    if (clear && input) input.value = "";
    if (!input?.value.trim()) form.classList.remove("is-expanded");
    if (document.activeElement && form.contains(document.activeElement)) document.activeElement.blur();
  }

  function closeEmptySearches() {
    document.querySelectorAll(".appbar-search").forEach((form) => closeSearch(form));
  }

  function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text);
    }
    const input = document.createElement("textarea");
    input.value = text;
    input.style.position = "fixed";
    input.style.left = "-9999px";
    document.body.appendChild(input);
    input.select();
    document.execCommand("copy");
    input.remove();
    return Promise.resolve();
  }

  function highlightCode() {
    if (window.hljs) {
      document.querySelectorAll(".post-content pre:not(.mermaid) code").forEach((block) => {
        hljs.highlightElement(block);
      });
    }
  }

  function wrapTables() {
    document.querySelectorAll(".post-content table").forEach((table) => {
      if (table.parentElement.classList.contains("table-wrapper")) return;
      const wrapper = document.createElement("div");
      wrapper.className = "table-wrapper";
      table.parentNode.insertBefore(wrapper, table);
      wrapper.appendChild(table);
    });
  }

  function codeCopy() {
    document.querySelectorAll(".post-content pre:not(.mermaid)").forEach((pre) => {
      if (pre.querySelector(".post-content-code-copy")) return;
      const button = document.createElement("button");
      button.type = "button";
      button.className = "post-content-code-copy";
      button.title = "复制";
      button.appendChild(icon("content-copy"));
      button.addEventListener("click", () => {
        const text = pre.querySelector("code")?.textContent || pre.textContent || "";
        copyText(text).then(() => {
          button.title = "已复制";
          window.setTimeout(() => (button.title = "复制"), 1200);
        });
      });
      pre.appendChild(button);
    });
  }

  function showProgress() {
    if (!progress) return;
    progress.classList.remove("is-done");
    progress.classList.add("is-active");
  }

  function hideProgress() {
    if (!progress) return;
    progress.classList.remove("is-active");
    progress.classList.add("is-done");
    window.setTimeout(() => progress.classList.remove("is-done"), 280);
  }

  function shouldPjax(link) {
    if (body.dataset.pjax !== "1") return false;
    if (!link || link.target || link.hasAttribute("download")) return false;
    if (link.closest(".post-content-code-copy")) return false;
    const url = new URL(link.href, window.location.href);
    if (url.origin !== window.location.origin) return false;
    if (url.pathname.startsWith("/admin") || url.pathname.startsWith("/uploads")) return false;
    if (url.pathname.startsWith("/theme/") || url.pathname === "/comment") return false;
    if (url.pathname.endsWith(".xml") || url.pathname.endsWith(".json")) return false;
    if (url.pathname === window.location.pathname && url.search === window.location.search && url.hash) return false;
    return true;
  }

  function replaceFromDocument(doc) {
    const nextContainer = doc.querySelector("#pjax-container");
    const currentContainer = document.querySelector("#pjax-container");
    if (!nextContainer || !currentContainer) return false;
    currentContainer.replaceWith(nextContainer);

    const nextToolbar = doc.querySelector(".mdui-toolbar");
    const currentToolbar = document.querySelector(".mdui-toolbar");
    if (nextToolbar && currentToolbar) currentToolbar.replaceWith(nextToolbar);

    const nextBody = doc.body;
    body.dataset.pjax = nextBody.dataset.pjax || body.dataset.pjax;
    body.dataset.infiniteScroll = nextBody.dataset.infiniteScroll || "0";
    body.dataset.themeMode = nextBody.dataset.themeMode || body.dataset.themeMode;
    document.title = doc.title;
    return true;
  }

  async function loadPage(url, push) {
    showProgress();
    try {
      const response = await fetch(url, { headers: { "X-PJAX": "true" } });
      if (!response.ok) throw new Error(response.statusText);
      const html = await response.text();
      const doc = new DOMParser().parseFromString(html, "text/html");
      if (!replaceFromDocument(doc)) {
        window.location.href = url;
        return;
      }
      if (push) history.pushState({ pjax: true }, "", url);
      window.scrollTo({ top: 0, behavior: "auto" });
      afterLoad();
    } catch (err) {
      window.location.href = url;
    } finally {
      hideProgress();
    }
  }

  async function appendNextPage(link) {
    if (pageAppendPending) return;
    pageAppendPending = true;
    const originalLabel = link.textContent;
    link.classList.add("is-loading");
    link.textContent = "加载中...";
    showProgress();
    try {
      const response = await fetch(link.href, { headers: { "X-PJAX": "true" } });
      if (!response.ok) throw new Error(response.statusText);
      const html = await response.text();
      const doc = new DOMParser().parseFromString(html, "text/html");
      const currentArticle = document.querySelector(".article");
      const nextPosts = doc.querySelectorAll(".article .post");
      if (!currentArticle || !nextPosts.length) {
        await loadPage(link.href, true);
        return;
      }
      nextPosts.forEach((post) => currentArticle.appendChild(post));
      const nextLoad = doc.querySelector(".changePage-load");
      const currentLoad = document.querySelector(".changePage-load");
      if (nextLoad && currentLoad) {
        currentLoad.href = nextLoad.href;
      } else {
        const end = doc.querySelector(".changePage:not(.changePage-load)");
        currentLoad?.replaceWith(end || document.createTextNode(""));
      }
    } catch (err) {
      window.location.href = link.href;
    } finally {
      if (link.isConnected) {
        link.classList.remove("is-loading");
        link.textContent = originalLabel;
      }
      pageAppendPending = false;
      hideProgress();
      initInfiniteScroll();
    }
  }

  function initInfiniteScroll() {
    infiniteObserver?.disconnect();
    infiniteObserver = null;
    if (body.dataset.infiniteScroll !== "1") return;
    const link = document.querySelector(".changePage-load");
    if (!link || !("IntersectionObserver" in window)) return;
    infiniteObserver = new IntersectionObserver((entries) => {
      if (entries.some((entry) => entry.isIntersecting)) {
        appendNextPage(link);
      }
    }, { rootMargin: "320px 0px" });
    infiniteObserver.observe(link);
  }

  function bindGlobalEvents() {
    document.addEventListener("click", (event) => {
      const target = event.target instanceof Element ? event.target : event.target.parentElement;
      if (!target) return;

      if (!target.closest(".appbar-search")) {
        closeEmptySearches();
      }

      if (target.closest(".drawer-toggle")) {
        event.preventDefault();
        openDrawer();
        return;
      }
      if (target.closest(".drawer-overlay") || target.closest(".drawer-list a")) {
        closeDrawer();
      }
      if (target.closest(".top")) {
        event.preventDefault();
        window.scrollTo({ top: 0, behavior: "smooth" });
        return;
      }
      if (target.closest(".brightness")) {
        event.preventDefault();
        const dark = !body.classList.contains("mdui-theme-layout-dark");
        localStorage.setItem("cuckoo-brightness", dark ? "dark" : "light");
        applyTheme(dark ? "dark" : "light");
        return;
      }
      const searchClose = target.closest(".appbar-search-close");
      if (searchClose) {
        event.preventDefault();
        const form = searchClose.closest(".appbar-search");
        closeSearch(form, true);
        return;
      }
      const searchIcon = target.closest(".appbar-search-icon");
      if (searchIcon) {
        const form = searchIcon.closest(".appbar-search");
        const input = form?.querySelector("input");
        if (form && input && !form.classList.contains("is-expanded")) {
          event.preventDefault();
          form.classList.add("is-expanded");
          input.focus();
          return;
        }
        if (input && !input.value.trim()) {
          event.preventDefault();
          input.focus();
          return;
        }
      }
      const loadMore = target.closest(".changePage-load");
      if (loadMore) {
        event.preventDefault();
        appendNextPage(loadMore);
        return;
      }

      const commentReply = target.closest("[data-comment-reply]");
      if (commentReply) {
        event.preventDefault();
        moveCommentForm(commentReply.dataset.parentId, commentReply.dataset.parentAuthor || "");
        return;
      }
      if (target.closest(".comment-cancel-reply")) {
        event.preventDefault();
        cancelCommentReply();
        return;
      }

      const link = target.closest("a");
      if (!shouldPjax(link)) return;
      event.preventDefault();
      closeDrawer();
      loadPage(link.href, true);
    });

    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape") {
        closeDrawer();
        closeEmptySearches();
      }
    });

    document.addEventListener("focusin", (event) => {
      const target = event.target instanceof Element ? event.target : null;
      const form = target?.closest(".appbar-search");
      form?.classList.add("is-expanded");
    });

    window.addEventListener("scroll", () => {
      refreshBackTop();
    }, { passive: true });

    window.addEventListener("popstate", () => loadPage(window.location.href, false));
    window.matchMedia?.("(prefers-color-scheme: dark)")?.addEventListener?.("change", () => applyTheme());
  }

  function afterLoad() {
    applyTheme();
    refreshSearch();
    destroyTOC();
    initTOC();
    highlightCode();
    wrapTables();
    codeCopy();
    initCommentDraft();
    initCommentSubmit();
    initCommentReply();
    initCommentAvatarPreview();
    initInfiniteScroll();
    refreshBackTop();
    closeDrawer();
  }

  setAccent();
  bindGlobalEvents();
  afterLoad();
})();
