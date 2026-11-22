// Draws the TeX formulas of a note page and of the editor preview with KaTeX, vendored in
// static/katex/. The server renderer (internal/infra/render/math.go) and the preview put the TeX
// source of each formula into a .math-inline or .math-display element.
//
// CSP-safe: katex.render builds DOM nodes and sets their styles through CSSOM, which the page
// CSP permits. katex.renderToString is not used, because its style="..." attributes are blocked.
//
// The script tag gives the KaTeX URLs in data-katex-js and data-katex-css. When KaTeX is not on
// the page yet, the first formula loads it from this server. With data-math-root, the script
// draws the formulas in that element when it runs.
(function () {
  "use strict";

  var SELECTOR = ".math-inline, .math-display";
  // trust: false refuses \href, \url and \htmlClass; maxSize and maxExpand limit what one formula
  // can make the browser do. strict: "ignore" keeps LaTeX-compatibility notes out of the console.
  var OPTIONS = { throwOnError: true, trust: false, strict: "ignore", maxSize: 50, maxExpand: 1000 };

  var script = document.currentScript;
  var loading = null;

  function load() {
    if (window.katex) {
      return Promise.resolve();
    }

    if (!loading) {
      loading = new Promise(function (resolve, reject) {
        var style = document.createElement("link");
        style.rel = "stylesheet";
        style.href = script.dataset.katexCss;
        document.head.appendChild(style);

        var code = document.createElement("script");
        code.src = script.dataset.katexJs;
        code.nonce = script.nonce;
        code.onload = resolve;
        code.onerror = reject;
        document.head.appendChild(code);
      });
    }

    return loading;
  }

  // draw replaces the TeX source of each formula with the drawn formula. A formula that KaTeX
  // cannot parse keeps its source, gets the math-error class and shows the reason on hover.
  function draw(root) {
    var formulas = root.querySelectorAll(SELECTOR);
    for (var i = 0; i < formulas.length; i++) {
      var node = formulas[i];
      if (node.dataset.tex !== undefined) {
        continue;
      }

      var tex = node.textContent;
      node.dataset.tex = tex;
      OPTIONS.displayMode = node.classList.contains("math-display");

      try {
        window.katex.render(tex, node, OPTIONS);
      } catch (err) {
        node.textContent = tex;
        node.classList.add("math-error");
        node.title = String(err && err.message ? err.message : err);
      }
    }
  }

  function render(root) {
    if (!root || !root.querySelector(SELECTOR)) {
      return;
    }

    load().then(
      function () {
        draw(root);
      },
      function () {
        /* KaTeX did not load; the formulas keep their TeX source. */
      },
    );
  }

  window.padmarkMath = { render: render };

  if (script.dataset.mathRoot) {
    render(document.querySelector(script.dataset.mathRoot));
  }
})();
