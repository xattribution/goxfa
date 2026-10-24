// The goxfa viewer: draws a laid-out XFA form (the Doc JSON from Go) as
// pages of SVG with real inputs over the fields.
//
//   XFAViewer.render(el, doc, {onChange(som, value), signatureText, zoom})
//
// Printed text is SVG text stretched to the width Go measured, so lines
// break and align where the layout put them, whatever fonts the browser
// has. Inputs sit exactly on the fields' boxes.
(function () {
  "use strict";
  const PX = 96 / 72; // CSS pixels per point
  const NS = "http://www.w3.org/2000/svg";

  function el(tag, attrs, parent) {
    const e = document.createElementNS(NS, tag);
    for (const k in attrs) if (attrs[k] !== undefined && attrs[k] !== null) e.setAttribute(k, attrs[k]);
    if (parent) parent.appendChild(e);
    return e;
  }
  function h(tag, cls, parent) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (parent) parent.appendChild(e);
    return e;
  }
  function family(f) {
    const t = (f.typeface || "").toLowerCase();
    let fb = "Arial, Helvetica, sans-serif";
    if (/times|georgia|garamond|minion|palatino|cambria|schoolbook/.test(t)) fb = "'Times New Roman', Times, serif";
    else if (/courier|mono|consolas/.test(t)) fb = "'Courier New', Courier, monospace";
    return "'" + (f.typeface || "Arial").replace(/'/g, "") + "', " + fb;
  }
  function dash(e) {
    const w = e.w || 0.5;
    switch (e.dash) {
      case "dashed": return [w * 3, w * 3].join(" ");
      case "dotted": return [w, w * 2].join(" ");
      case "dashDot": return [w * 3, w * 2, w, w * 2].join(" ");
      case "dashDotDot": return [w * 3, w * 2, w, w * 2, w, w * 2].join(" ");
    }
    return null;
  }
  function stroke(node, e) {
    node.setAttribute("stroke", e.color || "#000");
    node.setAttribute("stroke-width", e.w);
    const d = dash(e);
    if (d) node.setAttribute("stroke-dasharray", d);
  }

  function drawLines(svg, fonts, lines, ox, oy) {
    for (const l of lines || []) {
      for (const r of l.runs || []) {
        if (!r.text) continue;
        const f = fonts[r.font] || { size: 10 };
        const t = el("text", {
          x: ox + l.x + r.x, y: oy + l.y,
          "font-family": family(f), "font-size": f.size,
          "font-weight": f.bold ? "bold" : null, "font-style": f.italic ? "italic" : null,
          fill: f.color || "#000",
          "text-decoration": f.underline ? "underline" : (f.strike ? "line-through" : null),
        }, svg);
        if (r.w > 0.5 && r.text.trim()) {
          t.setAttribute("textLength", r.w);
          t.setAttribute("lengthAdjust", "spacingAndGlyphs");
        }
        t.textContent = r.text;
      }
    }
  }

  function drawItem(ctx, it) {
    const { svg, layer, fonts } = ctx;
    switch (it.t) {
      case "rect": {
        const edges = it.edges || [];
        const r = el("rect", { x: it.x, y: it.y, width: Math.max(0, it.w), height: Math.max(0, it.h), rx: it.r || null, fill: it.fill || "none" }, svg);
        if (edges.length === 1) {
          if (edges[0].w > 0) stroke(r, edges[0]); else r.setAttribute("stroke", "none");
        } else if (edges.length === 4) {
          const x = it.x, y = it.y, w = it.w, hh = it.h;
          const pts = [[x, y, x + w, y], [x + w, y, x + w, y + hh], [x + w, y + hh, x, y + hh], [x, y + hh, x, y]];
          edges.forEach((e, i) => {
            if (!(e.w > 0)) return;
            const p = pts[i];
            stroke(el("line", { x1: p[0], y1: p[1], x2: p[2], y2: p[3] }, svg), e);
          });
        }
        break;
      }
      case "line": {
        const e = (it.edges || [])[0] || { w: 0.5 };
        if (e.w > 0) stroke(el("line", { x1: it.x, y1: it.y, x2: it.x2, y2: it.y2 }, svg), e);
        break;
      }
      case "ellipse": {
        const e = (it.edges || [])[0];
        const c = el("ellipse", { cx: it.x + it.w / 2, cy: it.y + it.h / 2, rx: it.w / 2, ry: it.h / 2, fill: it.fill || "none" }, svg);
        if (e && e.w > 0) stroke(c, e);
        break;
      }
      case "text":
        drawLines(svg, fonts, it.lines, it.x, it.y);
        break;
      case "image":
        el("image", { x: it.x, y: it.y, width: it.w, height: it.h, href: it.src, preserveAspectRatio: "xMidYMid meet" }, svg);
        break;
      case "field":
        drawField(ctx, it);
        break;
      case "group": {
        const g = h("div", "xfa-group", layer);
        g.style.left = it.x * PX + "px";
        g.style.top = it.y * PX + "px";
        g.style.transform = "rotate(" + -it.rot + "deg)";
        const gsvg = el("svg", { class: "xfa-svg", width: 1, height: 1, viewBox: "0 0 0.75 0.75", overflow: "visible" }, g);
        gsvg.style.overflow = "visible";
        const sub = { svg: gsvg, layer: g, fonts, opts: ctx.opts, inputs: ctx.inputs };
        for (const k of it.items || []) drawItem(sub, k);
        break;
      }
    }
  }

  function place(node, it) {
    node.style.left = it.x * PX + "px";
    node.style.top = it.y * PX + "px";
    node.style.width = Math.max(0, it.w) * PX + "px";
    node.style.height = Math.max(0, it.h) * PX + "px";
  }

  function drawField(ctx, it) {
    const f = it.field, fonts = ctx.fonts, opts = ctx.opts;
    const font = fonts[f.font] || { size: 10 };
    const fire = (v) => opts.onChange && opts.onChange(f.som, v, f);
    let node;
    switch (f.kind) {
      case "check":
      case "radio": {
        node = h("button", "xfa-check xfa-" + f.shape, ctx.layer);
        node.type = "button";
        const on = f.value === f.on && f.on !== "";
        node.setAttribute("role", f.kind === "radio" ? "radio" : "checkbox");
        node.setAttribute("aria-checked", on ? "true" : "false");
        node.textContent = on ? ({ check: "✓", circle: "●", cross: "✕", diamond: "◆", square: "■", star: "★" }[f.mark] || "✓") : "";
        node.style.fontSize = Math.max(it.h * 0.8, 6) * PX + "px";
        if (!f.readOnly) node.onclick = () => fire(f.kind === "radio" ? (on ? "" : f.on) : (on ? "0" : "1"));
        break;
      }
      case "choice": {
        node = h("select", "xfa-input", ctx.layer);
        const blank = h("option", "", node);
        blank.value = "";
        (f.options || []).forEach((o, i) => {
          const op = h("option", "", node);
          op.value = f.saves ? f.saves[i] : o;
          op.textContent = o;
        });
        node.value = f.value || "";
        if (node.value !== (f.value || "")) {
          const op = h("option", "", node);
          op.value = f.value; op.textContent = f.display || f.value; node.value = f.value;
        }
        node.onchange = () => fire(node.value);
        break;
      }
      case "multiline":
        node = h("textarea", "xfa-input", ctx.layer);
        node.value = f.display || f.value || "";
        node.onchange = () => fire(node.value);
        break;
      case "signature":
        node = h("div", "xfa-signature", ctx.layer);
        node.textContent = opts.signatureText || "Sign in Acrobat";
        break;
      case "button":
        node = h("div", "xfa-button", ctx.layer);
        node.textContent = f.display || "";
        break;
      case "image":
      case "barcode":
        return;
      default: {
        node = h("input", "xfa-input", ctx.layer);
        node.type = f.kind === "password" ? "password" : "text";
        if (f.kind === "date" && /^\d{4}-\d{2}-\d{2}$/.test(f.value || "") || (f.kind === "date" && !f.value)) {
          node.type = "date";
          node.value = f.value || "";
        } else {
          node.value = f.display || f.value || "";
        }
        if (f.maxChars) node.maxLength = f.maxChars;
        if (f.comb) {
          node.maxLength = f.comb;
          node.classList.add("xfa-comb");
          const cell = it.w / f.comb;
          node.style.letterSpacing = Math.max(0, cell - font.size * 0.6) * PX + "px";
          node.style.paddingLeft = Math.max(0, (cell - font.size * 0.6) / 2) * PX + "px";
        }
        node.onchange = () => fire(node.value);
        break;
      }
    }
    place(node, it);
    node.dataset.som = f.som;
    if (f.tip) node.title = f.tip;
    if (f.readOnly) { node.disabled = true; node.classList.add("xfa-readonly"); }
    if (f.required) node.classList.add("xfa-required");
    if (node.tagName === "INPUT" || node.tagName === "TEXTAREA" || node.tagName === "SELECT") {
      node.style.fontFamily = family(font);
      node.style.fontSize = font.size * PX + "px";
      node.style.fontWeight = font.bold ? "bold" : "normal";
      node.style.fontStyle = font.italic ? "italic" : "normal";
      node.style.color = font.color || "#000";
      node.style.textAlign = f.comb ? "left" : (f.hAlign === "center" ? "center" : f.hAlign === "right" ? "right" : "left");
    }
    ctx.inputs.push(node);
    if (it.noPrint) node.classList.add("xfa-noprint");
  }

  // fit shrinks pages to the window's width on narrow screens.
  function fit(root) {
    const avail = root.clientWidth - 16;
    for (const p of root.querySelectorAll(".xfa-page")) {
      const w = parseFloat(p.style.width);
      p.style.zoom = avail > 0 && w > avail ? String(avail / w) : "";
    }
  }

  function render(root, doc, opts) {
    opts = opts || {};
    const focus = document.activeElement && document.activeElement.dataset ? document.activeElement.dataset.som : null;
    root.textContent = "";
    root.classList.add("xfa-doc");
    const fonts = doc.fonts || [];
    (doc.pages || []).forEach((p, pi) => {
      const page = h("div", "xfa-page", root);
      page.style.width = p.w * PX + "px";
      page.style.height = p.h * PX + "px";
      page.setAttribute("aria-label", "Page " + (pi + 1));
      const svg = el("svg", { class: "xfa-svg", width: p.w * PX, height: p.h * PX, viewBox: "0 0 " + p.w + " " + p.h }, page);
      const layer = h("div", "xfa-layer", page);
      const ctx = { svg, layer, fonts, opts, inputs: [] };
      for (const it of p.items || []) {
        if (it.noPrint && opts.print) continue;
        drawItem(ctx, it);
      }
    });
    fit(root);
    if (!root.xfaFit) {
      root.xfaFit = true;
      window.addEventListener("resize", () => fit(root));
    }
    if (focus) {
      const n = root.querySelector('[data-som="' + CSS.escape(focus) + '"]');
      if (n) n.focus();
    }
  }

  window.XFAViewer = { render };
})();
