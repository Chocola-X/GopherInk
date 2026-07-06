(function () {
  function ready(fn) {
    if (document.readyState === "loading") {
      document.addEventListener("DOMContentLoaded", fn);
    } else {
      fn();
    }
  }

  ready(function () {
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
