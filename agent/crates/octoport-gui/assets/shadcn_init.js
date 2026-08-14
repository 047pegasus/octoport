// Recharts chart engine for the OctoPort GUI.
//
// React, ReactDOM, prop-types and recharts are eval'd into the webview by Rust
// before this script runs (all UMD globals: React, ReactDOM, PropTypes, Recharts).
// This script mounts one React root per `div[data-chart-values]` container and
// renders the chart kind implied by `data-name` (AreaChart, PieChart,
// RadarChart, RadialChart). Live data arrives imperatively from Rust through
// `window.__octoportUpdateCharts(batch)`.
//
// ---------------------------------------------------------------------------
// ROOT CAUSE THIS FILE EXISTS TO FIX
// ---------------------------------------------------------------------------
// The old contract was `__octoportUpdateChart(id, valuesJson, labelsJson,
// namesJson)` where the three payloads were expected to be *JSON strings* that
// this side would `JSON.parse`. But the Rust call site interpolated the
// serialized JSON straight into the JS source unquoted:
//
//     window.__octoportUpdateChart('realtime-chart',[[1,2,3]],["-1s","now"],["a"]);
//
// so JS received genuine Arrays, not strings. `JSON.parse` coerces its
// argument with String() first, which turns [[1,2,3]] into "1,2,3" — not valid
// JSON — so it threw, and the throw landed in a bare `catch { return false; }`.
// Every single update was silently discarded. Charts therefore only ever
// displayed the static placeholder baked into their mount-time data-* attrs:
// the pie stuck at 50/50, the radial at 50/50, the radar at all-zeros (grid
// but no shape), and the area chart empty. Nothing was ever partially working.
//
// The fix is `coerce()` below: accept already-parsed values *or* JSON strings.
// Everything else in this file removes the surrounding fragility that made the
// bug so hard to see (silent catches, drop-on-not-yet-mounted, mount-time-only
// sizing, and a wait-then-push handshake that could deadlock a push loop
// forever).
(() => {
  if (window.__octoportShadcnDone) return;
  window.__octoportShadcnDone = true;

  const ReactObj = window.React || (typeof React !== "undefined" ? React : null);
  const ReactDOMObj = window.ReactDOM || (typeof ReactDOM !== "undefined" ? ReactDOM : null);
  if (!ReactObj || !ReactDOMObj) {
    console.error("[octoport] React or ReactDOM not available");
    return;
  }
  const { createElement: h } = ReactObj;
  const getR = () => window.Recharts || (typeof Recharts !== "undefined" ? Recharts : null);

  // Fixed palette shared with Rust's series_color_for() so chart series, the
  // usage-table row dots and the legend chips always agree on a tunnel's hue.
  const PALETTE = ["#CCCCFF", "#6EE7B7", "#FBBF24", "#F472B6", "#60A5FA", "#A78BFA", "#FB923C", "#34D399"];
  const seriesColor = (i) => PALETTE[i % PALETTE.length];

  // Per-chart record: { el, root, name, values, labels, names, colors, w, h }.
  const charts = new Map();
  // Latest payload per chart id, retained even when the chart is not mounted
  // yet (or is being remounted because Dioxus replaced the container node).
  // mount() seeds itself from here, so an update that arrives "too early" is
  // applied as soon as the chart exists instead of being thrown away.
  const pending = new Map();

  let updateCount = 0;
  let dropCount = 0;
  let lastError = null;

  // Accept either an already-parsed value (Array/Object — what Rust actually
  // sends) or a JSON string (the old contract). This is the root-cause fix;
  // see the header comment.
  function coerce(v, fallback) {
    if (v === null || v === undefined) return fallback;
    if (typeof v === "string") {
      const t = v.trim();
      if (!t) return fallback;
      try {
        return JSON.parse(t);
      } catch (e) {
        lastError = "coerce: " + e.message;
        return fallback;
      }
    }
    return v;
  }

  const cssVar = (name, fallback) => {
    const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
    return v || fallback;
  };

  function chromeColors() {
    return {
      grid: cssVar("--color-chart-grid", "rgba(148,163,184,0.25)"),
      fg: cssVar("--color-foreground", "#e2e8f0"),
      muted: cssVar("--color-muted-foreground", "#94a3b8"),
    };
  }

  function fmtNum(v) {
    if (v == null || isNaN(v)) return "0";
    const abs = Math.abs(v);
    if (abs >= 1000000) return (v / 1000000).toFixed(2).replace(/\.?0+$/, "") + "M";
    if (abs >= 1000) return (v / 1000).toFixed(1).replace(/\.0$/, "") + "k";
    if (v >= 100 || v <= -100 || Number.isInteger(v)) return Math.round(v).toLocaleString();
    return v.toFixed(1);
  }

  function TooltipBox(props) {
    const { active, payload, label } = props;
    if (!active || !payload || !payload.length) return null;
    const c = chromeColors();
    const rows = payload.filter((p) => p.value !== null && p.value !== undefined);
    if (!rows.length) return null;
    return h(
      "div",
      {
        style: {
          background: "rgba(17,17,27,0.92)",
          border: "1px solid " + c.grid,
          borderRadius: "6px",
          padding: "6px 10px",
          fontSize: "11px",
          fontFamily: "var(--mono, monospace)",
          color: c.fg,
          boxShadow: "0 4px 12px rgba(0,0,0,0.3)",
        },
      },
      [
        h("div", { key: "l", style: { color: c.muted, marginBottom: "3px" } }, String(label)),
        ...rows.map((p, i) =>
          h(
            "div",
            { key: i, style: { display: "flex", alignItems: "center", gap: "6px" } },
            h("span", { style: { width: 8, height: 8, borderRadius: 2, background: p.color, display: "inline-block" } }),
            h("span", {}, p.name + ": " + fmtNum(p.value))
          )
        ),
      ]
    );
  }

  // Measure the container. Recharts needs explicit pixel dimensions here (we
  // deliberately don't use ResponsiveContainer, which needs a settled layout
  // pass of its own and misbehaves inside Dioxus' dangerous_inner_html host).
  // Returns null when the element has no usable box yet — the caller then
  // skips this frame rather than baking in a bogus fallback size forever,
  // which is what the previous mount-time-only measurement did.
  function measure(el) {
    const r = el.getBoundingClientRect();
    const w = Math.round(r.width) || el.offsetWidth || (el.parentElement && el.parentElement.clientWidth) || 0;
    const hh = Math.round(r.height) || el.offsetHeight || (el.parentElement && el.parentElement.clientHeight) || 0;
    if (w < 60 || hh < 40) return null;
    return { w, h: hh };
  }

  // Centred "No data" state. Drawn instead of a chart when the payload carries
  // no information at all -- an all-null/all-zero series, or a pie over zero
  // bytes. Rendering those as a chart shows a flat line pinned to the axis or
  // an even split ring, both of which read as real measurements.
  function buildEmpty(size, text) {
    const c = chromeColors();
    return h(
      "div",
      {
        style: {
          width: size.w + "px",
          height: size.h + "px",
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          color: c.muted,
          fontFamily: "var(--mono, monospace)",
          fontSize: "12px",
          letterSpacing: "0.04em",
        },
      },
      text || "No data"
    );
  }

  // ---- chart builders (each returns a React element) ----

  // Values may contain nulls: Rust left-pads every series with null (not 0) so
  // a tunnel that started late, or a window that isn't full yet, renders as a
  // GAP rather than as fake zero traffic. Plotting those pads as 0 is what
  // produced the "negative seconds showing activity before the tunnel existed"
  // artifact on the x-axis.
  function buildArea(el, values, labels, names, colors, size, ticks) {
    const R = getR();
    if (!R) return null;
    const isMulti = Array.isArray(values[0]);
    const series = isMulti ? values : [values];
    const points = labels.map((label, i) => {
      const p = { label };
      series.forEach((s, si) => {
        const v = s ? s[i] : null;
        p["v" + si] = v === null || v === undefined ? null : v;
      });
      return p;
    });
    const c = chromeColors();
    const col = (i) => (colors && colors[i]) || seriesColor(i);
    const axisTick = { fill: c.muted, fontSize: 10 };
    // Rust supplies the exact label subset to draw, so the axis always shows
    // round, meaningful elapsed times. Falling back to interval thinning would
    // pick whatever labels happen to land on the stride.
    const tickProps =
      ticks && ticks.length ? { ticks, interval: 0 } : { interval: Math.max(0, Math.ceil(Math.max(points.length, 1) / 6) - 1) };
    // IMPORTANT: children MUST be passed as spread varargs to createElement,
    // NOT as a { children } prop. Recharts v2 walks children via
    // React.Children utilities; passing an array as a prop makes it miss the
    // Area/CartesianGrid/Axis elements entirely and render an empty chart.
    return h(
      R.AreaChart,
      { width: size.w, height: size.h, data: points, margin: { top: 6, right: 8, bottom: 0, left: 0 } },
      h(
        "defs",
        {},
        ...series.map((_, si) =>
          h(
            "linearGradient",
            { key: si, id: "grad-" + el.id + "-" + si, x1: "0", y1: "0", x2: "0", y2: "1" },
            h("stop", { offset: "0%", stopColor: col(si), stopOpacity: 0.38 }),
            h("stop", { offset: "100%", stopColor: col(si), stopOpacity: 0.02 })
          )
        )
      ),
      h(R.CartesianGrid, { stroke: c.grid, strokeWidth: 1, vertical: false }),
      h(R.XAxis, {
        dataKey: "label",
        tick: axisTick,
        tickLine: false,
        axisLine: { stroke: c.grid },
        minTickGap: 8,
        ...tickProps,
      }),
      h(R.YAxis, {
        width: 44,
        tick: axisTick,
        tickLine: false,
        axisLine: false,
        allowDecimals: false,
        domain: [0, (max) => Math.max(4, Math.ceil((max || 0) * 1.25))],
      }),
      h(R.Tooltip, { content: TooltipBox, isAnimationActive: false }),
      ...series.map((_, si) =>
        h(R.Area, {
          key: si,
          // "monotone" is a shape-preserving cubic: smooth curves with no
          // overshoot or wobble between points. Combined with the Rust-side
          // smoothing pass this gives the soft, non-jagged line requested.
          type: "monotone",
          dataKey: "v" + si,
          name: (names && names[si]) || "Series " + (si + 1),
          stroke: col(si),
          strokeWidth: 2,
          fill: "url(#grad-" + el.id + "-" + si + ")",
          fillOpacity: 1,
          dot: false,
          activeDot: { r: 3, strokeWidth: 0 },
          connectNulls: false,
          isAnimationActive: false,
        })
      )
    );
  }

  function buildPie(el, values, labels, colors, size) {
    const R = getR();
    if (!R) return null;
    const nums = (values || []).map((v) => Number(v) || 0);
    const sum = nums.reduce((a, b) => a + b, 0);
    // All-zero data would give Recharts 0/0 arc geometry (NaN paths); fall
    // back to an even split so the ring still draws as an "idle" state.
    const points = labels.map((l, i) => ({ name: l, value: sum === 0 ? 1 : nums[i] || 0 }));
    const c = chromeColors();
    const col = (i) => (colors && colors[i]) || seriesColor(i);
    return h(
      R.PieChart,
      { width: size.w, height: size.h },
      h(
        R.Pie,
        {
          data: points,
          dataKey: "value",
          nameKey: "name",
          cx: "50%",
          cy: "50%",
          innerRadius: "55%",
          outerRadius: "85%",
          paddingAngle: sum === 0 ? 0 : 2,
          stroke: "none",
          isAnimationActive: false,
        },
        ...points.map((p, i) => h(R.Cell, { key: i, fill: col(i), stroke: "none" }))
      ),
      h(R.Tooltip, { content: TooltipBox, isAnimationActive: false }),
      h(R.Legend, { verticalAlign: "bottom", wrapperStyle: { fontSize: 11, color: c.fg, paddingTop: 6 } })
    );
  }

  function buildRadar(el, values, labels, colors, size) {
    const R = getR();
    if (!R) return null;
    const points = labels.map((l, i) => ({ name: l, value: Number(values[i]) || 0 }));
    const c = chromeColors();
    const col = (colors && colors[0]) || seriesColor(0);
    return h(
      R.RadarChart,
      { width: size.w, height: size.h, data: points, cx: "50%", cy: "50%", outerRadius: "72%" },
      h(R.PolarGrid, { stroke: c.grid }),
      h(R.PolarAngleAxis, { dataKey: "name", tick: { fill: c.muted, fontSize: 10 } }),
      // Fixed 0-100 domain: every axis is already normalised to a percentage
      // by Rust, so an auto domain would rescale the whole shape every tick
      // and make an idle tunnel look identical to a saturated one.
      h(R.PolarRadiusAxis, { angle: 90, domain: [0, 100], tick: false, axisLine: false }),
      h(R.Radar, {
        name: "Health",
        dataKey: "value",
        stroke: col,
        strokeWidth: 2,
        fill: col,
        fillOpacity: 0.25,
        isAnimationActive: false,
      }),
      h(R.Tooltip, { content: TooltipBox, isAnimationActive: false })
    );
  }

  function buildRadial(el, values, labels, colors, size) {
    const R = getR();
    if (!R) return null;
    const points = labels.map((l, i) => ({ name: l, value: Number(values[i]) || 0 }));
    const c = chromeColors();
    const col = (i) => (colors && colors[i]) || seriesColor(i);
    return h(
      R.RadialBarChart,
      {
        width: size.w,
        height: size.h,
        data: points,
        cx: "50%",
        cy: "50%",
        innerRadius: "28%",
        outerRadius: "92%",
        startAngle: 90,
        endAngle: -270,
        barSize: 10,
      },
      // Explicit 0-100 angle domain turns this into a true gauge. Without it
      // Recharts scales the arcs against the largest value present, so a
      // single 100% series always drew a full ring no matter the real load.
      h(R.PolarAngleAxis, { type: "number", domain: [0, 100], angleAxisId: 0, tick: false }),
      h(
        R.RadialBar,
        {
          dataKey: "value",
          cornerRadius: 6,
          background: { fill: c.grid },
          isAnimationActive: false,
        },
        ...points.map((p, i) => h(R.Cell, { key: i, fill: col(i) }))
      ),
      h(R.Tooltip, { content: TooltipBox, isAnimationActive: false }),
      h(R.Legend, { verticalAlign: "bottom", wrapperStyle: { fontSize: 11, color: c.fg, paddingTop: 6 } })
    );
  }

  // ---- render / mounts ----

  function kindOf(name) {
    const n = (name || "").replace(/Chart$/, "").toLowerCase();
    if (n === "pie") return "pie";
    if (n === "radar") return "radar";
    if (n === "radial") return "radial";
    return "area";
  }

  function render(rec) {
    const { el, name, values, labels, names, colors, ticks } = rec;
    if (!el.isConnected) return;
    // Re-measure on EVERY render, not just at mount. The container's box is
    // often not settled when Dioxus first injects it (and changes when the
    // window or the modal resizes), so a single mount-time measurement locked
    // charts to a stale or fallback size for their whole lifetime.
    const size = measure(el);
    if (!size) {
      scheduleRetry();
      return;
    }
    rec.w = size.w;
    rec.h = size.h;

    let element;
    try {
      if (rec.empty) {
        element = buildEmpty(size, rec.emptyText);
      } else {
        switch (kindOf(name)) {
          case "pie":
            element = buildPie(el, values, labels, colors, size);
            break;
          case "radar":
            element = buildRadar(el, values, labels, colors, size);
            break;
          case "radial":
            element = buildRadial(el, values, labels, colors, size);
            break;
          default:
            element = buildArea(el, values, labels, names, colors, size, ticks);
        }
      }
    } catch (err) {
      lastError = "[build] " + String((err && err.message) || err);
      console.error("[octoport] chart build failed for", el.id, err);
      return;
    }
    if (!element) {
      lastError = "[build] recharts not available";
      return;
    }
    // NEVER touch rec.el's DOM directly (e.g. innerHTML = "") once a React
    // root owns it. `rec.root` is a persistent React 18 createRoot() bound to
    // this exact container; React tracks the nodes it created and reconciles
    // against them on every .render(). Clearing the container out from under
    // it rips out nodes React still thinks it owns, so the next .render()
    // throws while patching against nodes that no longer exist. root.render()
    // alone is sufficient to diff and patch.
    try {
      rec.root.render(element);
    } catch (err) {
      lastError = "[render] " + String((err && err.message) || err);
      console.error("[octoport] root.render threw for", el.id, err);
    }
  }

  // Charts whose container had no usable box yet get one cheap retry pass.
  let retryTimer = null;
  function scheduleRetry() {
    if (retryTimer) return;
    retryTimer = setTimeout(() => {
      retryTimer = null;
      for (const rec of charts.values()) {
        if (rec.el.childElementCount === 0) render(rec);
      }
    }, 120);
  }

  function mount(el) {
    const id = el.id;
    if (!id) return;
    // Seed from the latest pushed payload if one already arrived, so a
    // remount (Dioxus replacing the container node) resumes with live data
    // instead of snapping back to the static mount-time placeholder.
    const buffered = pending.get(id);
    const rec = {
      el,
      root: ReactDOMObj.createRoot(el),
      name: el.dataset.name || "AreaChart",
      values: buffered ? buffered.values : coerce(el.dataset.chartValues, []),
      labels: buffered ? buffered.labels : coerce(el.dataset.chartLabels, []),
      ticks: buffered ? buffered.ticks : coerce(el.dataset.chartTicks, []),
      names: buffered ? buffered.names : coerce(el.dataset.chartSeriesNames, []),
      colors: buffered ? buffered.colors : coerce(el.dataset.chartColors, []),
      empty: buffered ? buffered.empty : coerce(el.dataset.chartEmpty, false) === true,
      emptyText: buffered ? buffered.emptyText : el.dataset.chartEmptyText || "No data",
      w: 0,
      h: 0,
    };
    charts.set(id, rec);
    if (typeof ResizeObserver !== "undefined") {
      rec.ro = new ResizeObserver(() => {
        const s = measure(el);
        if (s && (s.w !== rec.w || s.h !== rec.h)) render(rec);
      });
      try {
        rec.ro.observe(el);
      } catch (e) {
        /* ignore */
      }
    }
    render(rec);
  }

  function unmount(id) {
    const rec = charts.get(id);
    if (!rec) return;
    if (rec.ro) {
      try {
        rec.ro.disconnect();
      } catch (e) {
        /* ignore */
      }
    }
    // Defer unmount: React 18 warns (and can throw) if a root is unmounted
    // synchronously from inside a lifecycle/commit triggered by the same
    // MutationObserver batch that detected the removal.
    const root = rec.root;
    setTimeout(() => {
      try {
        root.unmount();
      } catch (e) {
        /* ignore */
      }
    }, 0);
    charts.delete(id);
  }

  // Mount every in-DOM container, unmount vanished ones, and re-render any
  // whose svg was cleared by a Dioxus repaint. Dioxus may replace the
  // container element itself (fresh node, same id), so re-create the root
  // whenever the tracked element no longer matches the live node.
  function sync() {
    document.querySelectorAll("div[data-chart-values]").forEach((el) => {
      if (!el.id) return;
      const rec = charts.get(el.id);
      if (!rec) {
        mount(el);
      } else if (rec.el !== el) {
        unmount(el.id);
        mount(el);
      } else if (rec.el.childElementCount === 0) {
        // Container was blanked by a Dioxus repaint. NOTE: check for any
        // child, not for an <svg> -- the "No data" state renders a plain div,
        // and an svg-only check would re-render it on every mutation forever.
        render(rec);
      }
    });
    for (const id of Array.from(charts.keys())) {
      if (!document.getElementById(id)) unmount(id);
    }
  }

  // ---- imperative hooks (Rust call sites live in app.rs) ----

  // Apply one chart's payload. `values`/`labels`/`names`/`colors` may be
  // native arrays (what Rust sends) or JSON strings (legacy contract) —
  // coerce() handles both. Payloads for charts that aren't mounted yet are
  // buffered rather than dropped, so push ordering never matters and no
  // handshake is required before pushing.
  function applyOne(id, p) {
    const payload = {
      values: coerce(p.values, []),
      labels: coerce(p.labels, []),
      ticks: coerce(p.ticks, []),
      names: coerce(p.names, []),
      colors: coerce(p.colors, []),
      empty: coerce(p.empty, false) === true,
      emptyText: p.emptyText || "No data",
    };
    pending.set(id, payload);
    const rec = charts.get(id);
    if (!rec) {
      dropCount++;
      return false;
    }
    Object.assign(rec, payload);
    updateCount++;
    render(rec);
    return true;
  }

  window.__octoportUpdateChart = (id, values, labels, names, colors) =>
    applyOne(id, { values, labels, names, colors });

  // Batched entry point: one webview round-trip per frame for every visible
  // chart, instead of one eval per chart. `batch` is
  // { id: { values, labels, names, colors }, ... }.
  window.__octoportUpdateCharts = (batch) => {
    const b = coerce(batch, null);
    if (!b || typeof b !== "object") return 0;
    let n = 0;
    for (const id of Object.keys(b)) {
      if (applyOne(id, b[id] || {})) n++;
    }
    return n;
  };

  // Kept for compatibility; resolves (never rejects) so a caller can't be
  // deadlocked. Rust no longer gates its push loop on this — a rejection used
  // to kill a push loop permanently for the lifetime of the component.
  window.__octoportWaitChart = (id, timeoutMs = 5000) =>
    new Promise((resolve) => {
      const start = Date.now();
      const check = () => {
        if (charts.has(id)) return resolve(true);
        if (Date.now() - start > timeoutMs) return resolve(false);
        setTimeout(check, 50);
      };
      check();
    });

  window.__octoportCharts = () =>
    JSON.stringify({
      ready: document.readyState,
      libs: { react: !!ReactObj, reactdom: !!ReactDOMObj, recharts: !!getR() },
      divs: Array.from(document.querySelectorAll("div[data-chart-values]")).map((d) => ({
        id: d.id,
        name: d.dataset.name,
      })),
      instances: Array.from(charts.entries()).map(([id, rec]) => ({
        id,
        size: [rec.w, rec.h],
        painted: rec.el.childElementCount > 0,
        empty: !!rec.empty,
        points: Array.isArray(rec.labels) ? rec.labels.length : 0,
      })),
      pending: Array.from(pending.keys()),
      updates: updateCount,
      drops: dropCount,
      lastError,
      theme: document.documentElement.dataset.theme,
    });

  // ---- DOM observation ----

  let debounce;
  const observer = new MutationObserver(() => {
    clearTimeout(debounce);
    debounce = setTimeout(sync, 50);
  });
  observer.observe(document.body, { childList: true, subtree: true });

  // Theme flips change --chart-*/--color-* vars; re-render so chrome tracks.
  let themeDebounce;
  const themeObserver = new MutationObserver(() => {
    clearTimeout(themeDebounce);
    themeDebounce = setTimeout(() => {
      for (const rec of charts.values()) render(rec);
    }, 30);
  });
  themeObserver.observe(document.documentElement, {
    attributes: true,
    attributeFilter: ["data-theme", "class", "style", "data-color-theme"],
  });

  let resizeTimer;
  window.addEventListener("resize", () => {
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(() => {
      for (const rec of charts.values()) render(rec);
    }, 150);
  });

  function boot() {
    sync();
  }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", boot);
  } else {
    boot();
  }
  setTimeout(boot, 100);
  setTimeout(boot, 500);
  window.addEventListener("load", boot);
})();
