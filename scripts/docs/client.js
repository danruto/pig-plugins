(() => {
  // The dialog's scroll-lock stylesheet uses the bundler nonce.
  window.__webpack_nonce__ = document.currentScript.nonce;
  let resolveStyle;
  const styleLoaded = new Promise((resolve) => { resolveStyle = resolve; });
  const settleStyle = (event) => {
    if (event.target.id === "renderer-style") resolveStyle(event.type === "load");
  };
  document.addEventListener("load", settleStyle, true);
  document.addEventListener("error", settleStyle, true);
  document.addEventListener("DOMContentLoaded", async () => {
    const handles = [];
    const scheme = document.getElementById("scheme");
    scheme.addEventListener("change", () => {
      document.documentElement.dataset.docsScheme = scheme.value;
      for (const handle of handles) handle.setScheme(scheme.value);
    });
    window.addEventListener("pagehide", (event) => {
      if (!event.persisted) for (const handle of handles) handle.unmount();
    });
    const api = window.AquaCutterRender;
    const ready = api && await styleLoaded;
    document.removeEventListener("load", settleStyle, true);
    document.removeEventListener("error", settleStyle, true);
    let heading = document.title;
    for (const element of document.querySelectorAll("article h1, article h2, article h3, article .diagram")) {
      if (!element.classList.contains("diagram")) {
        heading = element.textContent;
        continue;
      }
      const status = element.querySelector(".diagram-status");
      if (!ready) {
        status.textContent = "Aqua Cutter assets unavailable. Diagram source remains below.";
        continue;
      }
      try {
        const mounted = await api.mountDiagram(
          element.querySelector(".diagram-reader"),
          { source: element.querySelector("code").textContent, description: heading },
          { scheme: scheme.value },
        );
        if (!mounted.ok) {
          status.textContent = `Aqua Cutter could not render this diagram (${mounted.issues.map((issue) => issue.code).join(", ")}). Source remains below.`;
          continue;
        }
        handles.push(mounted.handle);
        mounted.handle.setScheme(scheme.value);
        const details = document.createElement("details");
        const summary = document.createElement("summary");
        summary.textContent = "Diagram source";
        details.append(summary, element.querySelector("pre"));
        element.append(details);
        status.hidden = true;
      } catch {
        status.textContent = "Aqua Cutter failed to render this diagram. Source remains below.";
      }
    }
  }, { once: true });
})();
