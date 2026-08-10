"use strict";

/* ---------- tiny helpers ---------- */

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => Array.from(document.querySelectorAll(sel));

async function api(path, opts = {}) {
  const init = { method: opts.method || "GET", headers: {} };
  if (opts.body !== undefined) {
    init.headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(opts.body);
  }
  const res = await fetch(path, init);
  if (res.status === 204) return null;
  const ct = res.headers.get("Content-Type") || "";
  const data = ct.includes("application/json") ? await res.json() : await res.text();
  if (!res.ok) {
    const msg = data && data.error ? data.error : `HTTP ${res.status}`;
    throw new Error(msg);
  }
  return data;
}

function toast(msg, isError = false) {
  const el = document.createElement("div");
  el.className = "toast" + (isError ? " error" : "");
  el.textContent = msg;
  $("#toasts").appendChild(el);
  setTimeout(() => el.remove(), 4500);
}

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") node.className = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else node.setAttribute(k, v);
  }
  for (const c of children) {
    node.append(c);
  }
  return node;
}

// Durations arrive as nanoseconds (Go time.Duration JSON encoding).
const nsToMs = (ns) => ns / 1e6;
const fmtMs = (ns) => (ns > 0 ? `${nsToMs(ns).toFixed(0)} ms` : "–");

const deviceKey = (d) => (d.info && d.info.id ? d.info.id : d.addr);
const deviceLabel = (d) => {
  const name = d.info && (d.info.name || d.info.id);
  return name ? `${name} (${d.addr})` : d.addr;
};

/* ---------- state ---------- */

const state = {
  devices: [],
  links: [],
  health: [],
};

/* ---------- navigation ---------- */

$$(".nav-btn").forEach((btn) => {
  btn.addEventListener("click", () => {
    $$(".nav-btn").forEach((b) => b.classList.toggle("active", b === btn));
    $$(".view").forEach((v) => v.classList.remove("active"));
    $(`#view-${btn.dataset.view}`).classList.add("active");
  });
});

/* ---------- devices ---------- */

function latencyBadge(key) {
  const st = state.health.find((s) => s.device === key);
  if (!st) return el("span", { class: "badge grey" }, "no data");
  if (st.degraded) {
    // Degraded outranks "no data": a device that never answered a probe
    // is unreachable, not unknown.
    return el("span", { class: "badge red" }, st.samples === 0 ? "unreachable" : "degraded");
  }
  if (st.samples === 0) return el("span", { class: "badge grey" }, "no data");
  const ms = nsToMs(st.ewma);
  if (ms < 150) return el("span", { class: "badge green" }, fmtMs(st.ewma));
  if (ms < 300) return el("span", { class: "badge amber" }, fmtMs(st.ewma));
  return el("span", { class: "badge red" }, fmtMs(st.ewma));
}

function renderDevices() {
  const list = $("#device-list");
  list.replaceChildren();
  if (state.devices.length === 0) {
    list.append(el("p", { class: "muted" }, "No devices yet. Discover or add one below."));
    return;
  }
  for (const d of state.devices) {
    const key = deviceKey(d);
    const name = (d.info && (d.info.name || d.info.id)) || d.addr;
    const meta = [
      d.info && d.info.model ? d.info.model : "unknown model",
      d.info && d.info.gen ? `gen ${d.info.gen}` : null,
      d.source || null,
    ].filter(Boolean).join(" · ");
    list.append(
      el("div", { class: "device-card" },
        el("div", { class: "dev-name" }, name, latencyBadge(key)),
        el("div", { class: "dev-meta" }, d.addr),
        el("div", { class: "dev-meta" }, meta),
        el("div", { class: "dev-actions" },
          el("button", {
            class: "btn small danger",
            onclick: async () => {
              if (!confirm(`Remove device "${name}"?`)) return;
              try {
                await api(`/api/devices/${encodeURIComponent(key)}`, { method: "DELETE" });
                toast(`Removed ${name}`);
                await refreshDevices();
              } catch (err) {
                toast(err.message, true);
              }
            },
          }, "Remove"),
        ),
      ),
    );
  }
}

async function refreshDevices() {
  state.devices = await api("/api/devices");
  renderDevices();
  populateDeviceSelects();
}

$("#discover-btn").addEventListener("click", async (ev) => {
  const btn = ev.currentTarget;
  btn.disabled = true;
  const original = btn.textContent;
  btn.textContent = "Discovering…";
  try {
    const found = await api("/api/discover", { method: "POST", body: {} });
    toast(found.length ? `Found ${found.length} new device(s)` : "No new devices found");
    await refreshDevices();
  } catch (err) {
    toast(err.message, true);
  } finally {
    btn.disabled = false;
    btn.textContent = original;
  }
});

$("#add-device-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const form = ev.currentTarget;
  const body = {
    addr: form.addr.value.trim(),
    password: form.password.value,
  };
  try {
    const dev = await api("/api/devices", { method: "POST", body });
    toast(`Added ${deviceLabel(dev)}`);
    form.reset();
    await refreshDevices();
  } catch (err) {
    toast(err.message, true);
  }
});

function populateDeviceSelects() {
  for (const sel of $$("select[data-devices], select[data-devices-multi]")) {
    const prev = sel.multiple
      ? Array.from(sel.selectedOptions).map((o) => o.value)
      : sel.value;
    sel.replaceChildren();
    if (!sel.multiple) sel.append(el("option", { value: "" }, "— select device —"));
    for (const d of state.devices) {
      sel.append(el("option", { value: deviceKey(d) }, deviceLabel(d)));
    }
    if (sel.multiple) {
      for (const o of sel.options) o.selected = prev.includes(o.value);
    } else if (prev) {
      sel.value = prev;
    }
  }
}

/* ---------- links ---------- */

function renderLinks() {
  const body = $("#links-body");
  body.replaceChildren();
  if (state.links.length === 0) {
    body.append(el("tr", {}, el("td", { colspan: "5", class: "muted" }, "No links defined.")));
    return;
  }
  const nameFor = (key) => {
    const d = state.devices.find((x) => deviceKey(x) === key);
    return d ? ((d.info && (d.info.name || d.info.id)) || d.addr) : key;
  };
  for (const l of state.links) {
    const summary = `${nameFor(l.source_device)} [${l.source_component} ${l.source_event}] → ${nameFor(l.target_device)} ${l.target_method}`;
    const deployed = l.deployed_script_id
      ? el("span", { class: "badge green" }, `script #${l.deployed_script_id}`)
      : el("span", { class: "badge grey" }, "not deployed");
    let bleChip;
    if (l.fallback && l.fallback.ble_enabled) {
      bleChip = l.fallback.strategy === "race"
        ? el("span", { class: "chip on" }, "LAN + BLE race")
        : el("span", { class: "chip on" }, "BLE fallback");
    } else {
      bleChip = el("span", { class: "chip" }, "LAN only");
    }
    body.append(
      el("tr", {},
        el("td", {}, l.name || l.id),
        el("td", {}, summary),
        el("td", {}, bleChip),
        el("td", {}, deployed),
        el("td", { class: "actions" },
          el("button", { class: "btn small primary", onclick: () => deployLink(l) }, "Deploy"),
          el("button", { class: "btn small", onclick: () => viewScript(l) }, "View script"),
          el("button", { class: "btn small", onclick: () => editLink(l) }, "Edit"),
          el("button", {
            class: "btn small danger",
            onclick: async () => {
              if (!confirm(`Delete link "${l.name || l.id}"?`)) return;
              try {
                await api(`/api/links/${encodeURIComponent(l.id)}`, { method: "DELETE" });
                toast("Link deleted");
                await refreshLinks();
              } catch (err) {
                toast(err.message, true);
              }
            },
          }, "Delete"),
        ),
      ),
    );
  }
}

async function refreshLinks() {
  state.links = await api("/api/links");
  renderLinks();
}

async function deployLink(l) {
  try {
    await api(`/api/links/${encodeURIComponent(l.id)}/deploy`, { method: "POST" });
    toast(`Deployed "${l.name || l.id}"`);
    await refreshLinks();
  } catch (err) {
    toast(err.message, true);
  }
}

async function viewScript(l) {
  try {
    const res = await fetch(`/api/links/${encodeURIComponent(l.id)}/script`);
    if (!res.ok) {
      let msg = `HTTP ${res.status}`;
      try { msg = (await res.json()).error || msg; } catch { /* not json */ }
      throw new Error(msg);
    }
    $("#modal-title").textContent = `Script: ${l.name || l.id}`;
    $("#modal-pre").textContent = await res.text();
    $("#modal").classList.remove("hidden");
  } catch (err) {
    toast(err.message, true);
  }
}

function editLink(l) {
  const form = $("#link-form");
  $("#link-form-title").textContent = `Edit link: ${l.name || l.id}`;
  form.elements.id.value = l.id;
  form.elements.name.value = l.name || "";
  form.elements.source_device.value = l.source_device;
  form.elements.source_component.value = l.source_component || "input:0";
  form.elements.source_event.value = l.source_event || "toggle";
  form.elements.target_device.value = l.target_device;
  form.elements.target_method.value = l.target_method || "Switch.Toggle";
  form.elements.target_params.value = l.target_params ? JSON.stringify(l.target_params) : "";
  const f = l.fallback || {};
  form.elements.base_timeout_ms.value = f.base_timeout_ms || 400;
  form.elements.max_timeout_ms.value = f.max_timeout_ms || 1500;
  form.elements.latency_factor.value = f.latency_factor || 4;
  form.elements.ble_enabled.checked = !!f.ble_enabled;
  form.elements.strategy.value = f.strategy === "race" ? "race" : "fallback";
  form.scrollIntoView({ behavior: "smooth" });
}

function resetLinkForm() {
  const form = $("#link-form");
  form.reset();
  form.elements.id.value = "";
  $("#link-form-title").textContent = "New link";
}

$("#link-form-reset").addEventListener("click", resetLinkForm);

$("#link-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const form = ev.currentTarget;
  let params = null;
  const paramsText = form.elements.target_params.value.trim();
  if (paramsText) {
    try {
      params = JSON.parse(paramsText);
    } catch {
      toast("Target params is not valid JSON", true);
      return;
    }
  }
  const body = {
    id: form.elements.id.value || "",
    name: form.elements.name.value.trim(),
    source_device: form.elements.source_device.value,
    target_device: form.elements.target_device.value,
    source_component: form.elements.source_component.value.trim() || "input:0",
    source_event: form.elements.source_event.value,
    target_method: form.elements.target_method.value.trim() || "Switch.Toggle",
    target_params: params || undefined,
    fallback: {
      strategy: form.elements.strategy.value,
      ble_enabled: form.elements.ble_enabled.checked,
      base_timeout_ms: Number(form.elements.base_timeout_ms.value) || 0,
      max_timeout_ms: Number(form.elements.max_timeout_ms.value) || 0,
      latency_factor: Number(form.elements.latency_factor.value) || 0,
    },
  };
  if (body.fallback.strategy === "race" && /\.toggle$/i.test(body.target_method)) {
    toast("Race strategy needs an idempotent method — a Toggle fired over both LAN and BLE would undo itself. Use Switch.Set with explicit params, or the fallback strategy.", true);
    return;
  }
  try {
    await api("/api/links", { method: "POST", body });
    toast("Link saved");
    resetLinkForm();
    await refreshLinks();
  } catch (err) {
    toast(err.message, true);
  }
});

/* ---------- modal ---------- */

$("#modal-close").addEventListener("click", () => $("#modal").classList.add("hidden"));
$("#modal").addEventListener("click", (ev) => {
  if (ev.target === $("#modal")) $("#modal").classList.add("hidden");
});

/* ---------- health ---------- */

function renderHealth() {
  const body = $("#health-body");
  body.replaceChildren();
  if (state.health.length === 0) {
    body.append(el("tr", {}, el("td", { colspan: "6", class: "muted" }, "No health data yet.")));
    return;
  }
  for (const st of state.health) {
    const status = st.degraded
      ? el("span", { class: "badge red" }, "degraded")
      : el("span", { class: "badge green" }, "ok");
    body.append(
      el("tr", { class: st.degraded ? "degraded" : "" },
        el("td", {}, st.device),
        el("td", {}, fmtMs(st.ewma)),
        el("td", {}, fmtMs(st.last)),
        el("td", {}, String(st.samples)),
        el("td", {}, String(st.failures)),
        el("td", {}, status, st.last_error ? el("div", { class: "muted" }, st.last_error) : ""),
      ),
    );
  }
}

async function refreshHealth() {
  state.health = await api("/api/health");
  renderHealth();
  renderDevices(); // latency badges depend on health
}

$("#probe-btn").addEventListener("click", async (ev) => {
  const btn = ev.currentTarget;
  btn.disabled = true;
  try {
    state.health = await api("/api/health/probe", { method: "POST" });
    renderHealth();
    renderDevices();
    toast("Probe complete");
  } catch (err) {
    toast(err.message, true);
  } finally {
    btn.disabled = false;
  }
});

setInterval(() => {
  refreshHealth().catch(() => { /* transient network error; retry next tick */ });
  // The server auto-discovers devices in the background; keep the device
  // list in sync so new devices appear without a manual refresh.
  refreshDevices().catch(() => { /* transient network error; retry next tick */ });
}, 10000);

/* ---------- settings ---------- */

const selectedValues = (sel) => Array.from(sel.selectedOptions).map((o) => o.value);

$("#mqtt-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const form = ev.currentTarget;
  const body = {
    settings: {
      enable: form.elements.enable.checked,
      server: form.elements.server.value.trim(),
      user: form.elements.user.value.trim(),
      pass: form.elements.pass.value,
      topic_prefix: form.elements.topic_prefix.value.trim(),
      rpc_ntf: form.elements.rpc_ntf.checked,
      status_ntf: form.elements.status_ntf.checked,
    },
    devices: selectedValues(form.elements.devices),
  };
  try {
    await api("/api/settings/mqtt", { method: "POST", body });
    toast("MQTT settings applied");
  } catch (err) {
    toast(err.message, true);
  }
});

$("#ble-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const form = ev.currentTarget;
  const body = {
    settings: {
      enable: form.elements.enable.checked,
      rpc: form.elements.rpc.checked,
      observer: form.elements.observer.checked,
    },
    devices: selectedValues(form.elements.devices),
  };
  try {
    await api("/api/settings/ble", { method: "POST", body });
    toast("Bluetooth settings applied");
  } catch (err) {
    toast(err.message, true);
  }
});

$("#extender-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const enable = ev.submitter ? ev.submitter.dataset.enable === "true" : true;
  const device = ev.currentTarget.elements.device.value;
  if (!device) {
    toast("Pick a device first", true);
    return;
  }
  try {
    await api("/api/settings/extender", { method: "POST", body: { device, enable } });
    toast(`Range extender ${enable ? "enabled" : "disabled"}`);
  } catch (err) {
    toast(err.message, true);
  }
});

$("#join-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const form = ev.currentTarget;
  const body = {
    edge: form.elements.edge.value,
    extender: form.elements.extender.value,
  };
  if (!body.edge || !body.extender) {
    toast("Pick both an extender and an edge device", true);
    return;
  }
  try {
    await api("/api/settings/extender/join", { method: "POST", body });
    toast("Edge device joined to extender");
  } catch (err) {
    toast(err.message, true);
  }
});

$("#suggest-btn").addEventListener("click", async () => {
  const btn = $("#suggest-btn");
  const box = $("#suggestions");
  btn.disabled = true;
  box.textContent = "Reading WiFi signal strength from all devices…";
  try {
    const sugg = await api("/api/settings/extender/suggestions");
    box.replaceChildren();
    if (sugg.length === 0) {
      box.textContent = "No suggestions — every reachable device has decent WiFi signal.";
      return;
    }
    box.append(el("p", {},
      "RSSI measures signal to the router, not distance between devices — treat these as starting points. Click one to prefill the form:"));
    for (const s of sugg) {
      box.append(el("button", {
        class: "btn small",
        onclick: () => {
          const form = $("#join-form");
          form.elements.edge.value = s.edge;
          form.elements.extender.value = s.extender;
          toast(`Prefilled: ${s.edge} via ${s.extender}`);
        },
      }, `${s.edge} (${s.edge_rssi} dBm) ← ${s.extender} (${s.extender_rssi} dBm)`));
    }
  } catch (err) {
    box.textContent = "";
    toast(err.message, true);
  } finally {
    btn.disabled = false;
  }
});

/* ---------- boot ---------- */

(async function boot() {
  try {
    await refreshDevices();
    await Promise.all([refreshLinks(), refreshHealth()]);
  } catch (err) {
    toast(`Failed to load: ${err.message}`, true);
  }
})();
