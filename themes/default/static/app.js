(function () {
  const body = document.body;
  const progress = document.querySelector(".pjax-progress");
  const color = body.dataset.primary || "#ff4081";

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

  function buildTOC() {
    const toc = document.querySelector("#toc .toc");
    const content = document.querySelector(".post-content");
    const card = document.querySelector("#toc");
    if (!toc || !content || !card) return;
    const headings = Array.from(content.querySelectorAll("h1, h2, h3, h4"));
    toc.innerHTML = "";
    if (!headings.length) {
      card.remove();
      return;
    }
    const seen = new Map();
    headings.forEach((heading, index) => {
      if (!heading.id) heading.id = slugify(heading.textContent, index, seen);
      const link = document.createElement("a");
      link.href = "#" + heading.id;
      link.textContent = heading.textContent || "";
      if (heading.tagName === "H3") link.classList.add("toc-level-2");
      if (heading.tagName === "H4") link.classList.add("toc-level-3");
      toc.appendChild(link);
    });
    refreshTOC();
  }

  function refreshTOC() {
    const content = document.querySelector(".post-content");
    const toc = document.querySelector("#toc .toc");
    if (!content || !toc) return;
    const headings = Array.from(content.querySelectorAll("h1, h2, h3, h4"));
    const links = Array.from(toc.querySelectorAll("a"));
    let active = "";
    headings.forEach((heading) => {
      if (heading.getBoundingClientRect().top < 96) active = heading.id;
    });
    links.forEach((link) => link.classList.toggle("is-active", link.hash === "#" + active));
  }

  function refreshSearch() {
    document.querySelectorAll(".appbar-search").forEach((form) => {
      const input = form.querySelector("input");
      form.classList.toggle("is-expanded", Boolean(input?.value));
    });
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
        currentLoad?.replaceWith(doc.querySelector(".changePage") || document.createTextNode(""));
      }
    } catch (err) {
      window.location.href = link.href;
    } finally {
      hideProgress();
    }
  }

  function bindGlobalEvents() {
    document.addEventListener("click", (event) => {
      const target = event.target instanceof Element ? event.target : event.target.parentElement;
      if (!target) return;

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
        const input = form?.querySelector("input");
        if (input) input.value = "";
        form?.classList.remove("is-expanded");
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

      const link = target.closest("a");
      if (!shouldPjax(link)) return;
      event.preventDefault();
      closeDrawer();
      loadPage(link.href, true);
    });

    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape") {
        closeDrawer();
      }
    });

    window.addEventListener("scroll", () => {
      refreshBackTop();
      refreshTOC();
    }, { passive: true });

    window.addEventListener("popstate", () => loadPage(window.location.href, false));
    window.matchMedia?.("(prefers-color-scheme: dark)")?.addEventListener?.("change", () => applyTheme());
  }

  function afterLoad() {
    applyTheme();
    refreshSearch();
    buildTOC();
    codeCopy();
    refreshBackTop();
    closeDrawer();
  }

  setAccent();
  bindGlobalEvents();
  afterLoad();
})();
