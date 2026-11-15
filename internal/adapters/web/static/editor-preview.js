// Write / Split / Preview for the note editor (templates/index.html).
// The browser renders the preview with marked and sanitizes it with DOMPurify, both vendored
// next to this file. CSP-safe: loaded as an external 'self' script with the page nonce, no
// inline handlers, no eval.
(function () {
  "use strict";

  var STORAGE_KEY = "padmark_editor_view";
  var VIEWS = ["write", "split", "preview"];
  var PLACEHOLDER = "Nothing to preview yet.";
  // Same breakpoint as the phone-width block in style.css.
  var narrowQuery = window.matchMedia("(max-width: 640px)");

  var editor = document.getElementById("editor");
  var body = document.getElementById("editorBody");
  var preview = document.getElementById("preview");
  var tabList = document.getElementById("viewTabs");
  var tabs = Array.prototype.slice.call(tabList.querySelectorAll('[role="tab"]'));
  var formatButtons = document.querySelectorAll(".toolbar-actions button");

  // Keep the preview close to the server renderer (internal/infra/render/markdown.go): GFM
  // tables, strikethrough and autolinks, and a hard line break for each single newline. The
  // server has no task-list extension, so "- [ ]" stays literal text; the checkbox renderer
  // gives back the source text instead of an <input>.
  marked.use({
    gfm: true,
    breaks: true,
    renderer: {
      checkbox: function (token) {
        return escapeText(token.raw);
      },
    },
  });

  // SANITIZE_NAMED_PROPS puts user id/name attributes in the user-content- namespace, as the
  // server does, so the preview cannot shadow #editor, #charCount and the other page elements.
  // Inline style attributes and <style> are removed, because the page CSP blocks them. The
  // inert parser document of DOMPurify has the same CSP: a style attribute in the Markdown thus
  // logs one CSP report while DOMPurify parses it, but the style never applies.
  // RETURN_DOM_FRAGMENT gives the clean nodes, without a second parse of a string.
  var PURIFY_CONFIG = {
    SANITIZE_NAMED_PROPS: true,
    FORBID_TAGS: ["style", "form", "input", "button", "textarea", "select", "option"],
    FORBID_ATTR: ["style"],
    RETURN_DOM_FRAGMENT: true,
  };

  // A click on a preview link must not replace the editor page with unsaved text.
  DOMPurify.addHook("afterSanitizeAttributes", function (node) {
    if (node.tagName === "A" && node.hasAttribute("href")) {
      node.setAttribute("target", "_blank");
      node.setAttribute("rel", "noopener noreferrer nofollow");
    }
  });

  function escapeText(text) {
    return String(text).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  }

  function readStoredView() {
    try {
      var value = window.localStorage.getItem(STORAGE_KEY);
      return VIEWS.indexOf(value) >= 0 ? value : null;
    } catch (_) {
      return null; // Storage can be blocked; the default view applies.
    }
  }

  function storeView(view) {
    try {
      window.localStorage.setItem(STORAGE_KEY, view);
    } catch (_) {
      /* Storage can be blocked; the choice then lasts only for this page. */
    }
  }

  var chosen = readStoredView();
  var current = "write";
  var stale = true;
  var frame = 0;
  var timer = 0;
  var lastCost = 0;

  // currentView gives the view to show. Phone width has no room for Split, so it shows Write.
  function currentView() {
    var narrow = narrowQuery.matches;
    var view = chosen || (narrow ? "write" : "split");
    return narrow && view === "split" ? "write" : view;
  }

  function render() {
    stale = false;
    var started = performance.now();
    var text = editor.value;

    if (!text.trim()) {
      var empty = document.createElement("p");
      empty.className = "editor-preview-empty";
      empty.textContent = PLACEHOLDER;
      preview.replaceChildren(empty);
    } else {
      preview.replaceChildren(DOMPurify.sanitize(marked.parse(text, { async: false }), PURIFY_CONFIG));
    }

    lastCost = performance.now() - started;
  }

  // schedule renders at most once for each animation frame. When a render is slow (a large
  // note), the next one waits four times its cost, so typing keeps most of the main thread.
  function schedule() {
    stale = true;
    if (current === "write" || frame || timer) {
      return;
    }

    var delay = lastCost > 12 ? Math.min(600, Math.round(lastCost * 4)) : 0;
    var run = function () {
      timer = 0;
      frame = window.requestAnimationFrame(function () {
        frame = 0;
        render();
      });
    };

    if (delay) {
      timer = window.setTimeout(run, delay);
    } else {
      run();
    }
  }

  function apply(view) {
    current = view;
    body.dataset.view = view;
    editor.hidden = view === "preview";
    preview.hidden = view === "write";

    tabs.forEach(function (tab) {
      var selected = tab.dataset.view === view;
      tab.classList.toggle("active", selected);
      tab.setAttribute("aria-selected", String(selected));
      tab.tabIndex = selected ? 0 : -1;
      if (tab.dataset.view === "split") {
        tab.hidden = narrowQuery.matches;
      }
    });

    // The formatting buttons change the hidden textarea; in Preview they are off.
    for (var i = 0; i < formatButtons.length; i++) {
      formatButtons[i].disabled = view === "preview";
    }

    if (view !== "write" && stale) {
      render();
    }
  }

  function select(tab) {
    chosen = tab.dataset.view;
    storeView(chosen);
    apply(currentView());
  }

  function visibleTabs() {
    return tabs.filter(function (tab) {
      return !tab.hidden;
    });
  }

  tabs.forEach(function (tab) {
    tab.addEventListener("click", function () {
      select(tab);
    });
  });

  // Arrow keys, Home and End move the selection between the visible tabs (WAI-ARIA tabs pattern).
  tabList.addEventListener("keydown", function (event) {
    var visible = visibleTabs();
    var index = visible.indexOf(document.activeElement);
    if (index < 0) {
      return;
    }

    var next;
    if (event.key === "ArrowRight") {
      next = visible[(index + 1) % visible.length];
    } else if (event.key === "ArrowLeft") {
      next = visible[(index - 1 + visible.length) % visible.length];
    } else if (event.key === "Home") {
      next = visible[0];
    } else if (event.key === "End") {
      next = visible[visible.length - 1];
    } else {
      return;
    }

    event.preventDefault();
    next.focus();
    select(next);
  });

  editor.addEventListener("input", schedule);
  narrowQuery.addEventListener("change", function () {
    apply(currentView());
  });

  apply(currentView());
})();
