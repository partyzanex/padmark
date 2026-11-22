// Builds the "On this page" sidebar of a note page (templates/note.html) from the h2, h3 and h4
// headings of the rendered note. H2 rows are numbered "1", "2", H3 rows "1.1", "1.2"; H4 rows
// have no number, and H5-H6 are not in the list. Only the section that the reader is in is
// expanded; a chevron expands a different section, and "Expand all" expands all of them.
//
// CSP-safe: loaded as an external 'self' script with the page nonce, no inline handlers, no
// inline styles. The inline script of note.html runs first and gives window.padmarkNote: the
// "user-content-" id prefix and jump(), which scrolls to a heading and records the history entry.
// math.js runs after this script and draws the formulas of the list, because the list is in
// <main>, its data-math-root.
(function () {
  "use strict";

  var note = window.padmarkNote;
  var nav = document.getElementById("noteToc");
  var list = document.getElementById("noteTocList");
  var toggle = document.getElementById("noteTocToggle");
  var currentLabel = document.getElementById("noteTocCurrent");
  var allButton = document.getElementById("noteTocAll");
  var topLink = document.getElementById("noteTocTop");

  if (!note || !nav || !list) {
    return;
  }

  var prefix = note.prefix;
  var levels = { H2: 2, H3: 3, H4: 4 };
  var selector = ["h2", "h3", "h4"].map(function (tag) {
    return '.prose.markdown-body ' + tag + '[id^="' + prefix + '"]';
  }).join(", ");
  var headings = Array.prototype.filter.call(document.querySelectorAll(selector), function (heading) {
    return !heading.hidden && heading.textContent.trim() !== "";
  });

  // The column stays hidden when the note has no headings, so the article keeps its full width.
  if (headings.length === 0) {
    return;
  }

  var CHEVRON = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" ' +
    'stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m9 6 6 6-6 6"/></svg>';

  // headingLabel copies the text of a heading for the list. A formula keeps its TeX source in a
  // .math-inline element, so math.js draws it here too; textContent would show the TeX source, or,
  // once KaTeX has drawn the heading, its text twice.
  function headingLabel(node) {
    var label = document.createDocumentFragment();
    node.childNodes.forEach(function (child) {
      if (child.nodeType === Node.TEXT_NODE) {
        label.appendChild(document.createTextNode(child.textContent));
      } else if (child.nodeType !== Node.ELEMENT_NODE) {
        return;
      } else if (child.classList.contains("math-inline") || child.classList.contains("math-display")) {
        var formula = document.createElement("span");
        formula.className = "math-inline";
        formula.textContent = child.dataset.tex !== undefined ? child.dataset.tex : child.textContent;
        label.appendChild(formula);
      } else {
        label.appendChild(headingLabel(child));
      }
    });

    return label;
  }

  // plainText is the text of a heading for the title and aria-label attributes: a formula gives its
  // TeX source once, also after KaTeX has drawn it.
  function plainText(heading) {
    var span = document.createElement("span");
    span.appendChild(headingLabel(heading));

    return span.textContent.replace(/\s+/g, " ").trim();
  }

  // Each entry is {heading, level, item, link, sub, children, root}. A heading goes under the
  // nearest heading above it with a lower level. A heading without such a parent, for example an
  // h3 before the first h2, is a section of its own.
  var roots = [];
  var entries = [];
  var stack = [];

  headings.forEach(function (heading) {
    var level = levels[heading.tagName];
    while (stack.length > 0 && stack[stack.length - 1].level >= level) {
      stack.pop();
    }

    var parent = stack.length > 0 ? stack[stack.length - 1] : null;
    var entry = { heading: heading, level: level, parent: parent, children: [], sub: null };
    entry.root = parent ? parent.root : entry;
    (parent ? parent.children : roots).push(entry);
    entries.push(entry);
    stack.push(entry);
  });

  function number(entry) {
    if (!entry.parent) {
      return String(roots.indexOf(entry) + 1);
    }

    if (entry.parent === entry.root && entry.level === 3) {
      var siblings = entry.parent.children.filter(function (child) { return child.level === 3; });

      return number(entry.parent) + "." + (siblings.indexOf(entry) + 1);
    }

    // An h4 has no number in the list.
    return "";
  }

  // kind gives the row style: a section row, a numbered subsection row or an h4 row.
  function kind(entry) {
    if (!entry.parent) {
      return "h2";
    }

    return entry.parent === entry.root && entry.level === 3 ? "h3" : "h4";
  }

  function buildItem(entry) {
    var name = entry.heading.id.slice(prefix.length);
    var text = plainText(entry.heading);
    var item = document.createElement("li");
    item.className = "note-toc-item note-toc-item--" + kind(entry);

    var row = document.createElement("div");
    row.className = "note-toc-row";

    var link = document.createElement("a");
    link.className = "note-toc-link";
    link.href = "#" + name;
    link.title = text;

    var num = document.createElement("span");
    num.className = "note-toc-num";
    num.textContent = number(entry);
    num.setAttribute("aria-hidden", "true");

    // One element for the label: it clamps the text to two lines, and as loose flex items the
    // text pieces would lose their spaces at a formula.
    var label = document.createElement("span");
    label.className = "note-toc-label";
    label.appendChild(headingLabel(entry.heading));
    link.append(num, label);
    link.addEventListener("click", function (event) {
      // Close the phone drop-down first: it is in the page flow, and its height moves the target.
      setDropDown(false);
      if (note.jump(name, true)) {
        event.preventDefault();
        setCurrent(entry);
      }
    });
    row.appendChild(link);

    entry.item = item;
    entry.link = link;
    entry.text = text;

    if (!entry.parent && entry.children.length > 0) {
      var expand = document.createElement("button");
      expand.type = "button";
      expand.className = "note-toc-expand";
      var count = document.createElement("span");
      count.className = "note-toc-count";
      count.textContent = String(entry.children.length);
      expand.appendChild(count);
      expand.insertAdjacentHTML("beforeend", CHEVRON);
      expand.addEventListener("click", function () {
        // The chevron of the expanded section collapses it; a different chevron expands only its
        // own section, also after "Expand all".
        openRoot = openRoot === entry && !expandAll ? null : entry;
        expandAll = false;
        render();
      });
      row.appendChild(expand);
      entry.expand = expand;
    }

    item.appendChild(row);

    if (entry.children.length > 0) {
      var sub = document.createElement("ol");
      sub.className = "note-toc-sub";
      entry.children.forEach(function (child) { sub.appendChild(buildItem(child)); });
      item.appendChild(sub);
      entry.sub = sub;
    }

    return item;
  }

  roots.forEach(function (root) { list.appendChild(buildItem(root)); });

  var current = null;
  var openRoot = null;
  var expandAll = false;
  var hasSubsections = roots.some(function (root) { return root.children.length > 0; });

  if (allButton) {
    allButton.hidden = !hasSubsections;
    allButton.addEventListener("click", function () {
      expandAll = !expandAll;
      // "Collapse" goes back to the section that the reader is in.
      openRoot = current ? current.root : null;
      render();
    });
  }

  // render shows the sub-lists of the expanded sections and marks the current row and its section.
  // It does not scroll the list: after a click on a chevron the list stays where the reader put it.
  function render() {
    roots.forEach(function (root) {
      var open = expandAll || openRoot === root;
      root.item.classList.toggle("note-toc-item--section", current !== null && current.root === root);
      if (root.sub) {
        root.sub.hidden = !open;
      }
      if (root.expand) {
        root.expand.setAttribute("aria-expanded", String(open));
        root.expand.setAttribute("aria-label", (open ? "Collapse " : "Expand ") + root.text);
      }
    });

    entries.forEach(function (entry) {
      var on = entry === current;
      entry.link.classList.toggle("active", on);
      if (on) {
        entry.link.setAttribute("aria-current", "location");
      } else {
        entry.link.removeAttribute("aria-current");
      }
    });

    if (allButton) {
      allButton.textContent = expandAll ? "Collapse" : "Expand all";
      allButton.setAttribute("aria-expanded", String(expandAll));
    }
  }

  // revealCurrent scrolls the list, and only the list, so that the current row is visible.
  // scrollIntoView would also scroll the page. The bottom of the list fades out over 28px, so a row
  // in that band counts as hidden.
  function revealCurrent() {
    // A row in a collapsed section and a closed phone drop-down have no layout box.
    if (!current || list.scrollHeight <= list.clientHeight || current.link.getClientRects().length === 0) {
      return;
    }

    var fade = 28;
    var box = list.getBoundingClientRect();
    var row = current.link.getBoundingClientRect();
    if (row.top < box.top) {
      list.scrollTop += row.top - box.top - 4;
    } else if (row.bottom > box.bottom - fade) {
      list.scrollTop += row.bottom - box.bottom + fade;
    }
  }

  function setCurrent(entry) {
    if (entry === current) {
      return;
    }

    var sectionChanged = !current || current.root !== entry.root;
    current = entry;
    // A new section collapses the section that the reader left and expands the new one.
    if (sectionChanged) {
      openRoot = entry.root;
    }

    if (currentLabel) {
      currentLabel.replaceChildren(headingLabel(entry.heading));
      // math.js is deferred: before it runs, it draws the whole <main>, this label too.
      if (window.padmarkMath) {
        window.padmarkMath.render(currentLabel);
      }
    }

    render();
    revealCurrent();
  }

  // Narrow screens show the list as a drop-down under the toggle.
  function setDropDown(open) {
    if (!toggle) {
      return;
    }

    nav.classList.toggle("open", open);
    toggle.setAttribute("aria-expanded", String(open));
    if (open) {
      revealCurrent();
    }
  }

  if (toggle) {
    toggle.addEventListener("click", function () { setDropDown(!nav.classList.contains("open")); });
  }

  if (topLink) {
    topLink.addEventListener("click", function (event) {
      event.preventDefault();
      setDropDown(false);
      window.scrollTo(0, 0);
      if (location.hash) {
        history.pushState(null, "", location.pathname + location.search);
      }
    });
  }

  nav.hidden = false;

  // The current heading is the last heading above the top 40% of the viewport. At the end of a
  // scrolled page the last visible heading is current, because a short last section cannot get
  // to that line.
  function update() {
    var line = window.innerHeight * 0.4;
    var found = entries[0];
    entries.forEach(function (entry) {
      if (entry.heading.getBoundingClientRect().top <= line) {
        found = entry;
      }
    });

    var root = document.documentElement;
    if (window.scrollY > 0 && window.scrollY + window.innerHeight >= root.scrollHeight - 2) {
      entries.forEach(function (entry) {
        if (entry.heading.getBoundingClientRect().top < window.innerHeight) {
          found = entry;
        }
      });
    }

    setCurrent(found);
  }

  update();
  if ("IntersectionObserver" in window) {
    var observer = new IntersectionObserver(update, { rootMargin: "0px 0px -60% 0px", threshold: [0, 1] });
    entries.forEach(function (entry) { observer.observe(entry.heading); });

    // The footer comes into view at the end of the page, where no heading crosses the line.
    var footer = document.querySelector(".site-footer");
    if (footer) {
      new IntersectionObserver(update).observe(footer);
    }
  }
})();
