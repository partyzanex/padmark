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
    extensions: [mathBlockExtension(), mathInlineExtension()],
    renderer: {
      checkbox: function (token) {
        return escapeText(token.raw);
      },
    },
  });

  // The math extensions follow the rules of the server (internal/infra/render/math.go), so the
  // preview finds the same formulas: $…$, $$…$$ and \(…\) in a line of text, and a block that
  // starts a line with $$ or \[. A formula does not cross a backtick. static/math.js draws them.
  function mathBlockExtension() {
    var OPEN = /^ {0,3}(\$\$|\\\[)([^\n]*)(?:\n|$)/;

    return {
      name: "mathBlock",
      level: "block",
      start: function (src) {
        var match = /(?:^|\n) {0,3}(?:\$\$|\\\[)/.exec(src);
        if (!match) {
          return undefined;
        }

        return match[0].charAt(0) === "\n" ? match.index + 1 : match.index;
      },
      tokenizer: function (src) {
        var open = OPEN.exec(src);
        if (!open) {
          return undefined;
        }

        var closer = open[1] === "$$" ? "$$" : "\\]";
        var rest = open[2].replace(/\s+$/, "");

        if (rest.trim()) {
          // One line: the line ends with the closing delimiter, with TeX source before it.
          var inner = rest.slice(0, rest.length - closer.length);
          if (rest.length < closer.length || rest.slice(-closer.length) !== closer || !inner.trim()) {
            return undefined;
          }

          return { type: "mathBlock", raw: open[0], text: inner };
        }

        // Several lines, to the line that ends with the closing delimiter or to the end.
        var raw = open[0];
        var lines = [];
        var body = src.slice(raw.length);
        while (body) {
          var end = body.indexOf("\n");
          var line = end < 0 ? body : body.slice(0, end);
          var taken = end < 0 ? body : body.slice(0, end + 1);
          raw += taken;
          body = body.slice(taken.length);

          var trimmed = line.replace(/\s+$/, "");
          if (trimmed.slice(-closer.length) === closer) {
            var last = trimmed.slice(0, trimmed.length - closer.length);
            if (last.trim()) {
              lines.push(last);
            }
            break;
          }

          lines.push(line);
        }

        return { type: "mathBlock", raw: raw, text: lines.join("\n") };
      },
      renderer: function (token) {
        return '<div class="math-display">' + escapeText(token.text) + "</div>\n";
      },
    };
  }

  function mathInlineExtension() {
    // A backslash escapes the next character; an unescaped backtick ends the search.
    var DISPLAY = /^\$\$((?:\\[^\n]|[^\\$\n`])+)\$\$/;
    var DOLLAR = /^\$((?:\\[^\n]|[^\\$\n`])+)\$(?!\d)/;
    var PARENS = /^\\\(((?:\\[^()\n]|[^\\\n`])+)\\\)/;

    function token(raw, text, display) {
      return { type: "mathInline", raw: raw, text: text, display: display };
    }

    return {
      name: "mathInline",
      level: "inline",
      start: function (src) {
        var index = src.search(/\$|\\\(/);
        return index < 0 ? undefined : index;
      },
      tokenizer: function (src) {
        var match;
        if (src.slice(0, 2) === "$$") {
          match = DISPLAY.exec(src);
          return match && match[1].trim() ? token(match[0], match[1], true) : undefined;
        }

        if (src.charAt(0) === "$") {
          match = DOLLAR.exec(src);
          if (!match || /^\s|\s$/.test(match[1])) {
            return undefined;
          }

          return token(match[0], match[1], false);
        }

        match = PARENS.exec(src);
        return match && match[1].trim() ? token(match[0], match[1], false) : undefined;
      },
      renderer: function (token) {
        var name = token.display ? "math-display" : "math-inline";
        return '<span class="' + name + '">' + escapeText(token.text) + "</span>";
      },
    };
  }

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

  // The data: image rule of the server (dataImageProblem in infra/render/policy.go): the note
  // page shows only a base64 GIF, JPEG, PNG or WebP image with no query or fragment. DOMPurify
  // alone also admits SVG and other types, which the note page would not show.
  var RASTER_TYPE = /^image\/(?:gif|jpeg|png|webp)[;,]/i;
  var RASTER_PREFIX = /^image\/(?:gif|jpeg|png|webp);base64,/i;
  var SVG_TYPE = /^image\/svg\+xml[;,]/i;
  // Padded base64, as Go base64.StdEncoding decodes it.
  var BASE64 = /^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/;
  var CONTROL = /[\u0000-\u001f\u007f]/;

  // IMAGE_PROBLEMS gives the text for each reason key of the server.
  var IMAGE_PROBLEMS = {
    invalid: "the image address cannot be read.",
    address: "a data: image address cannot have a ? or # part.",
    svg: "SVG images are not supported. Use PNG, JPEG, GIF or WebP.",
    type: "only PNG, JPEG, GIF and WebP images are supported.",
    encoding: "the image data must be base64, with ;base64, right after the image type.",
    base64: "the base64 image data is damaged.",
  };

  // dataImageProblem gives the reason key why the note page does not show a data: image, or "".
  // The URL parser of the server removes the spaces around the address and its line breaks, and
  // refuses other control characters.
  function dataImageProblem(src) {
    var value = src.trim().replace(/[\r\n]/g, "");
    if (CONTROL.test(value)) {
      return "invalid";
    }

    var opaque = value.slice(5);
    var hash = opaque.indexOf("#");
    if (hash >= 0) {
      // The server removes an empty fragment and refuses a fragment with text.
      if (hash < opaque.length - 1) {
        return "address";
      }
      opaque = opaque.slice(0, hash);
    }
    if (opaque.indexOf("?") >= 0) {
      return "address";
    }
    if (SVG_TYPE.test(opaque)) {
      return "svg";
    }
    if (!RASTER_TYPE.test(opaque)) {
      return "type";
    }

    var prefix = RASTER_PREFIX.exec(opaque);
    if (!prefix) {
      return "encoding";
    }

    return BASE64.test(opaque.slice(prefix[0].length)) ? "" : "base64";
  }

  // droppedImages collects the images that the hook removes during one sanitize call; render
  // then puts a notice in place of each one, so the author sees why the note page will not
  // show it.
  var droppedImages = [];

  DOMPurify.addHook("afterSanitizeAttributes", function (node) {
    // A click on a preview link must not replace the editor page with unsaved text.
    if (node.tagName === "A" && node.hasAttribute("href")) {
      node.setAttribute("target", "_blank");
      node.setAttribute("rel", "noopener noreferrer nofollow");
    }
    if (node.tagName === "IMG" && node.hasAttribute("src")) {
      var src = node.getAttribute("src");
      var problem = /^\s*data:/i.test(src) ? dataImageProblem(src) : "";
      if (problem) {
        node.removeAttribute("src");
        droppedImages.push({ node: node, problem: problem });
      }
    }
  });

  // imageNotice makes the notice for an image that the note page will not show.
  function imageNotice(image, problem) {
    var notice = document.createElement("span");
    notice.className = "preview-image-notice";
    notice.setAttribute("role", "note");
    notice.dataset.problem = problem;

    var alt = image.getAttribute("alt");
    var title = document.createElement("strong");
    title.textContent = alt ? 'Image "' + alt + '" not shown: ' : "Image not shown: ";
    notice.append(title, IMAGE_PROBLEMS[problem]);

    return notice;
  }

  // sanitize cleans the HTML of marked and replaces each dropped image with its notice.
  function sanitize(html) {
    droppedImages = [];
    var fragment = DOMPurify.sanitize(html, PURIFY_CONFIG);
    droppedImages.forEach(function (dropped) {
      dropped.node.replaceWith(imageNotice(dropped.node, dropped.problem));
    });
    droppedImages = [];
    wrapTables(fragment);

    return fragment;
  }

  // TABLE_SCROLL_CLASS is the class of the box that scrolls a wide table (style.css).
  var TABLE_SCROLL_CLASS = "table-scroll";

  // wrapTables puts each outermost table in a scroll box, as the server does after its sanitizer
  // (wrapTables in internal/infra/render/table_scroll.go). A table in a table cell stays in the box
  // of the outer table. The server sanitizer removes the class from user HTML, so the preview
  // removes it from the user elements first.
  function wrapTables(fragment) {
    fragment.querySelectorAll("." + TABLE_SCROLL_CLASS).forEach(function (node) {
      node.classList.remove(TABLE_SCROLL_CLASS);
      if (!node.classList.length) {
        node.removeAttribute("class");
      }
    });

    fragment.querySelectorAll("table").forEach(function (table) {
      if (table.parentElement && table.parentElement.closest("table")) {
        return;
      }

      var box = document.createElement("div");
      box.className = TABLE_SCROLL_CLASS;
      table.replaceWith(box);
      box.append(table);
    });
  }

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
    // A render from apply() makes a scheduled one unnecessary. Without the cancel, the preview
    // renders twice and replaces its nodes again while the author reads them.
    window.clearTimeout(timer);
    window.cancelAnimationFrame(frame);
    timer = 0;
    frame = 0;
    stale = false;
    var started = performance.now();
    var text = editor.value;

    if (!text.trim()) {
      var empty = document.createElement("p");
      empty.className = "editor-preview-empty";
      empty.textContent = PLACEHOLDER;
      preview.replaceChildren(empty);
    } else if (body.dataset.contentType === "text/plain") {
      // The server shows a text/plain note as escaped text in <pre> (render.RenderPlain), without
      // Markdown and formulas. The HTML parser of the page removes one newline after <pre>;
      // textContent does not parse, so the preview removes that newline itself.
      var pre = document.createElement("pre");
      pre.textContent = text.charAt(0) === "\n" ? text.slice(1) : text;
      preview.replaceChildren(pre);
    } else {
      preview.replaceChildren(sanitize(marked.parse(text, { async: false })));
      // KaTeX draws after DOMPurify: its own output is trusted, the Markdown is not.
      window.padmarkMath.render(preview);
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
