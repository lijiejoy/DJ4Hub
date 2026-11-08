const $ = (selector) => document.querySelector(selector);
let lastSMSCount = null;
let esimHealthPollTimer = null;
let esimHealthInFlight = false;
let networkTrafficTimer = null;
let networkTrafficPrevious = null;
let networkTrafficInFlight = false;
let networkActivityTimer = null;
let networkActivityInFlight = false;
let networkActivityCountdown = 5;

function setThemePreference(theme) {
  if (theme === "light" || theme === "dark") {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem("dj4ghub-theme", theme);
    localStorage.removeItem("djonehub-theme");
    localStorage.removeItem("vohive-theme");
  } else {
    delete document.documentElement.dataset.theme;
    localStorage.removeItem("dj4ghub-theme");
    localStorage.removeItem("djonehub-theme");
    localStorage.removeItem("vohive-theme");
  }
  document.querySelectorAll("[data-theme-option]").forEach((button) => {
    button.setAttribute("aria-pressed", String(button.dataset.themeOption === theme));
  });
}

const savedTheme = localStorage.getItem("dj4ghub-theme") || localStorage.getItem("djonehub-theme") || localStorage.getItem("vohive-theme");
setThemePreference(savedTheme === "light" || savedTheme === "dark" ? savedTheme : "auto");
document.querySelectorAll("[data-theme-option]").forEach((button) => {
  button.addEventListener("click", () => setThemePreference(button.dataset.themeOption));
});

const operatorNames = new Map([
  ["CHN-UNICOM", "中国联通"],
  ["CHINA UNICOM", "中国联通"],
  ["UNICOM", "中国联通"],
  ["46001", "中国联通"],
  ["46006", "中国联通"],
  ["46009", "中国联通"],
  ["CHINA MOBILE", "中国移动"],
  ["CMCC", "中国移动"],
  ["CHN-CMCC", "中国移动"],
  ["46000", "中国移动"],
  ["46002", "中国移动"],
  ["46004", "中国移动"],
  ["46007", "中国移动"],
  ["46008", "中国移动"],
  ["CHINA TELECOM", "中国电信"],
  ["CHN-CT", "中国电信"],
  ["CTCC", "中国电信"],
  ["46003", "中国电信"],
  ["46005", "中国电信"],
  ["46011", "中国电信"],
  ["CBN", "中国广电"],
  ["CHN-CBN", "中国广电"],
  ["CHINA BROADNET", "中国广电"],
  ["46015", "中国广电"],
]);

async function api(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: { "Content-Type": "application/json", ...(options.headers || {}) },
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(data.error || `HTTP ${response.status}`);
  return data;
}

function notice(message) {
  const el = $("#notice");
  el.textContent = message;
  el.classList.add("show");
  clearTimeout(notice.timer);
  notice.timer = setTimeout(() => el.classList.remove("show"), 2600);
}

let modalResolve = null;

function closeModal(result = null) {
  const modal = $("#app-modal");
  modal.hidden = true;
  document.body.classList.remove("modal-open");
  if (modalResolve) {
    const resolve = modalResolve;
    modalResolve = null;
    resolve(result);
  }
}

function showModal({ title, message = "", fields = [], confirmLabel = "确定", danger = false }) {
  if (modalResolve) closeModal(null);
  const modal = $("#app-modal");
  const messageElement = $("#modal-message");
  const fieldsElement = $("#modal-fields");
  const confirmButton = $("#modal-confirm");
  $("#modal-title").textContent = title;
  messageElement.textContent = message;
  messageElement.hidden = !message;
  fieldsElement.replaceChildren(...fields.map((field) => {
    const label = document.createElement("label");
    label.className = "modal-field";
    const caption = document.createElement("span");
    caption.textContent = field.label;
    const input = document.createElement("input");
    input.name = field.name;
    input.value = field.value || "";
    input.placeholder = field.placeholder || "";
    input.autocomplete = "off";
    if (field.required) input.required = true;
    label.append(caption, input);
    return label;
  }));
  confirmButton.textContent = confirmLabel;
  confirmButton.className = danger ? "danger modal-danger" : "";
  modal.hidden = false;
  document.body.classList.add("modal-open");
  const firstInput = fieldsElement.querySelector("input");
  setTimeout(() => (firstInput || confirmButton).focus(), 0);
  return new Promise((resolve) => { modalResolve = resolve; });
}

$("#modal-form").addEventListener("submit", (event) => {
  event.preventDefault();
  const values = {};
  event.currentTarget.querySelectorAll(".modal-fields input").forEach((input) => {
    values[input.name] = input.value.trim();
  });
  closeModal(values);
});
$("#modal-cancel").addEventListener("click", () => closeModal(null));
$("#modal-close").addEventListener("click", () => closeModal(null));
$("#app-modal").addEventListener("click", (event) => {
  if (event.target === event.currentTarget) closeModal(null);
});
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && !$("#app-modal").hidden) closeModal(null);
});

async function copySMSCode(code) {
  try {
    await navigator.clipboard.writeText(code);
    notice(`验证码 ${code} 已复制`);
  } catch (error) {
    notice("复制失败，请手动复制验证码");
  }
}

function renderHardwareDetails(status) {
  const panel = $("#hardware-details");
  const device = status.usb_device;
  if (!device) {
    panel.hidden = true;
    panel.replaceChildren();
    return;
  }

  const title = document.createElement("strong");
  title.textContent = device ? "已检测到兼容 USB 设备" : "未检测到可用硬件";

  const detail = document.createElement("p");
  if (device) {
    const interfaceText = Array.isArray(device.interfaces)
      ? `${device.interfaces.length} 个 USB interface`
      : "interface 未知";
    detail.textContent = [
      `${device.vendor || "兼容设备"} ${device.product || ""}`.trim(),
      `${device.vendor_id}:${device.product_id}`,
      device.mode,
      interfaceText,
    ].filter(Boolean).join(" · ");
  } else {
    detail.textContent = status.discovery_error || "设备未枚举";
  }

  const hint = document.createElement("small");
  hint.textContent = status.discovery_error
    ? `当前限制：${status.discovery_error}`
    : "AT 串口可用后，短信和 eSIM/卡片操作会自动启用。";

  panel.hidden = false;
  panel.replaceChildren(title, detail, hint);
}

function setSidebarDeviceState(connected, device = null) {
  const panel = $("#sidebar-device");
  panel.classList.toggle("is-offline", !connected);
  $("#sidebar-device-name").textContent = connected
    ? (device?.product || "4G 模块")
    : "等待设备";
  $("#sidebar-device-state").textContent = connected ? "USB" : "未连接";
}

function setValue(id, text, tone = "") {
  const el = $(id);
  el.textContent = text || "--";
  el.className = tone;
}

function displayOperatorName(value) {
  const raw = String(value || "").trim();
  if (!raw) return "--";
  return operatorNames.get(raw.toUpperCase()) || raw;
}

function displayWorkMode(value) {
	if (value === null || value === undefined || value === "") {
	  return { label: "待读取", tone: "muted" };
	}
  switch (Number(value)) {
    case 0: return { label: "短信模式", tone: "neutral" };
    case 1: return { label: "上网模式", tone: "neutral" };
    case 2: return { label: "实验模式 2", tone: "warn" };
    case 3: return { label: "实验模式 3", tone: "warn" };
    default: return { label: "待读取", tone: "muted" };
  }
}

function setWorkModeControl(value) {
  const currentMode = value === null || value === undefined || value === "" ? -1 : Number(value);
  const smsButton = $("#workmode-sms");
  const networkButton = $("#workmode-network");
  smsButton.setAttribute("aria-pressed", currentMode === 0 ? "true" : "false");
  networkButton.setAttribute("aria-pressed", currentMode === 1 ? "true" : "false");
}

function setUSBNetModeSelector(value) {
  const currentMode = value === null || value === undefined || value === "" ? -1 : Number(value);
  [0, 1, 2, 3].forEach((mode) => {
    $("#usbnet-mode-" + mode).setAttribute("aria-pressed", currentMode === mode ? "true" : "false");
  });
}

function setHeaderDeviceState(connected, label = "设备在线") {
  const indicator = $("#header-device-state");
  indicator.classList.toggle("is-online", connected);
  indicator.classList.toggle("is-offline", !connected);
  indicator.querySelector("span").textContent = label;
}

async function loadSidebarConnection() {
  const panel = $("#sidebar-connection");
  try {
    const connection = await api("/api/network/local");
    if (!connection?.interface) {
      panel.hidden = true;
      return;
    }
    $("#sidebar-connection-detail").textContent = [connection.interface, connection.ipv4].filter(Boolean).join(" · ");
    const state = $("#sidebar-connection-state");
    state.textContent = connection.is_default ? "默认出口" : "已连接";
    state.classList.toggle("is-secondary", !connection.is_default);
    panel.hidden = false;
  } catch (_) {
    panel.hidden = true;
  }
}

function signalTone(dbm) {
  const value = Number(dbm);
  if (!Number.isFinite(value) || value === 0) return "muted";
  if (value >= -65) return "good";
  if (value >= -75) return "signal-fair";
  if (value >= -85) return "warn";
  if (value >= -95) return "orange";
  return "bad";
}

async function loadStatus() {
  try {
    const status = await api("/api/status");
    const connected = Boolean(status.usb_device || status.imei || status.firmware);
    setHeaderDeviceState(connected, connected ? "设备在线" : "等待设备");
    setSidebarDeviceState(connected, status.usb_device);
    setValue("#operator", displayOperatorName(status.operator), status.operator ? "neutral" : "muted");
    setValue("#signal", status.signal_dbm ? `${status.signal_dbm} dBm` : "--", signalTone(status.signal_dbm));
    setValue("#network-mode", status.network_mode || status.reg_status_text || "--", status.network_mode ? "neutral" : "muted");
    setValue(
      "#sim",
      status.sim_inserted ? "已插入" : (status.usb_device ? "待读取" : "未检测到"),
      status.sim_inserted ? "good" : (status.usb_device ? "warn" : "bad"),
    );
    const workMode = Object.prototype.hasOwnProperty.call(status, "usbnet_mode")
      ? displayWorkMode(status.usbnet_mode)
      : displayWorkMode(null);
    setValue("#work-mode", workMode.label, workMode.tone);
    setWorkModeControl(status.usbnet_mode);
    setUSBNetModeSelector(status.usbnet_mode);
    $("#device-summary").textContent = connected
      ? (status.hardware_status || [status.imei, status.firmware].filter(Boolean).join(" · ") || "模块初始化中")
      : "等待连接 4G 模块";
    renderHardwareDetails(status);
  } catch (error) {
    $("#device-summary").textContent = error.message;
    setHeaderDeviceState(false, "设备离线");
    setSidebarDeviceState(false);
    setWorkModeControl(null);
  }
}

async function loadSMS() {
  const list = $("#sms-list");
  try {
    const [messages, status] = await Promise.all([
      api("/api/sms"),
      api("/api/sms/status"),
    ]);
    const pollText = status.polling
      ? `自动轮询 ${status.poll_interval_s || 8}s`
      : "自动轮询未启用";
    const cleanupText = status.auto_cleanup_me ? "自动清理 ME 已开启" : "自动清理 ME 未开启";
    const errorText = status.last_poll_error ? ` · 最近错误：${status.last_poll_error}` : "";
    $("#sms-status").textContent = `当前缓存 ${messages.length} 条短信 · ${pollText} · ${cleanupText}${errorText}`;
    if (lastSMSCount !== null && messages.length > lastSMSCount) {
      notice(`收到 ${messages.length - lastSMSCount} 条新短信`);
    }
    lastSMSCount = messages.length;
    if (!messages.length) {
      list.className = "list empty";
      list.textContent = "暂无短信";
      return;
    }
    list.className = "list";
    list.replaceChildren(...messages.map((message) => {
      const row = document.createElement("article");
      row.className = "item";
      const sender = document.createElement("strong");
      sender.textContent = message.sender || "未知号码";
      const content = document.createElement("p");
      content.textContent = message.content;
      const time = document.createElement("time");
      time.textContent = new Date(message.timestamp).toLocaleString();
      if (message.code) {
        const actions = document.createElement("div");
        actions.className = "sms-actions";
        const badge = document.createElement("span");
        badge.className = "code-badge";
        badge.textContent = `验证码 ${message.code}`;
        const copy = document.createElement("button");
        copy.className = "secondary compact";
        copy.type = "button";
        copy.textContent = "复制";
        copy.addEventListener("click", () => copySMSCode(message.code));
        actions.append(badge, copy, time);
        row.append(sender, content, actions);
      } else {
        row.append(sender, content, time);
      }
      return row;
    }));
  } catch (error) {
    $("#sms-status").textContent = `读取列表失败：${error.message}`;
    notice(error.message);
  }
}

function profileRows(value) {
  const groups = Array.isArray(value) ? value : value?.profiles || [];
  return groups.flatMap((group) =>
    (group.profiles || []).map((profile) => ({ ...profile, aid: group.aid_hex || "" })),
  );
}

function profileDisplayName(profile) {
  return profile?.name || profile?.service_provider_name || profile?.iccid || "未命名 Profile";
}

function activeProfile(profiles) {
  return profiles.find((profile) => profile.state === 1) || null;
}

function maskIdentifier(value, keep = 4) {
  const text = String(value || "");
  if (text.length <= keep * 2) return text;
  return `${text.slice(0, keep)} ${"•".repeat(Math.max(4, text.length - keep * 2))} ${text.slice(-keep)}`;
}

function maskPhoneNumber(value) {
  const text = String(value || "").trim();
  const digitCount = [...text].filter((char) => /\d/.test(char)).length;
  if (digitCount <= 8) return text;
  let digitIndex = 0;
  return [...text].map((char) => {
    if (!/\d/.test(char)) return char;
    digitIndex += 1;
    return digitIndex > 4 && digitIndex <= digitCount - 4 ? "*" : char;
  }).join("");
}

async function copyIdentifier(value, label) {
  try {
    await navigator.clipboard.writeText(value);
    notice(`${label} 已复制`);
  } catch (error) {
    notice(`复制 ${label} 失败，请手动复制`);
  }
}

async function editProfileNote(profile, note) {
  const values = await showModal({
    title: "编辑模块资料",
    message: "这些资料保存在兼容模块中，并按 ICCID 与当前 Profile 关联。",
    confirmLabel: "保存",
    fields: [
      { name: "label", label: "模块内名称", value: note.label || "", placeholder: "可选" },
      { name: "phone", label: "模块号码", value: note.phone || "", placeholder: "可选" },
      { name: "tags", label: "用途标签", value: note.tags || "", placeholder: "例如：英国验证码" },
    ],
  });
  if (!values) return;
  try {
    await api("/api/esim/module-notes", {
      method: "PUT",
      body: JSON.stringify({ iccid: profile.iccid, label: values.label, phone: values.phone, tags: values.tags }),
    });
    notice("模块资料已保存");
    await loadESIM();
  } catch (error) {
    notice(error.message);
  }
}

function phonebookCheck(label, ok, detail) {
  const card = document.createElement("div");
  card.className = `phonebook-check ${ok ? "ok" : ""}`;
  const title = document.createElement("strong");
  title.textContent = label;
  const text = document.createElement("small");
  text.textContent = detail;
  card.append(title, text);
  return card;
}

async function probeESIMPhonebook() {
  const button = $("#probe-esim-phonebook");
  const status = $("#esim-phonebook-status");
  const resultPanel = $("#esim-phonebook-result");
  button.disabled = true;
  status.textContent = "正在检测卡内通讯录能力，不会写入联系人...";
  resultPanel.hidden = true;
  try {
    const result = await api("/api/esim/phonebook/probe", { method: "POST" });
    const supported = result.storage_supported && result.storage_selected;
    const portable = supported && result.read_supported && result.write_supported;
    status.textContent = portable
      ? "已确认当前 Profile 支持卡内通讯录读写；尚未写入任何联系人。"
      : "当前 Profile 未完整确认卡内通讯录读写能力；不会进行写入。";
    resultPanel.replaceChildren(
      phonebookCheck("SIM 通讯录", result.storage_supported, result.storage_supported ? "支持 SM 卡内存储" : "未发现 SM 卡内存储"),
      phonebookCheck("当前卡片", result.storage_selected, result.storage_selected ? "已安全选中 SM 存储" : "无法选中 SM 存储"),
      phonebookCheck("读取能力", result.read_supported, result.read_supported ? "模块支持读取卡内联系人" : "模块未确认读取命令"),
      phonebookCheck("写入接口", result.write_supported, result.write_supported ? "模块声明支持写入接口" : "模块未确认写入命令"),
      phonebookCheck("当前状态", supported, result.storage_status || "未返回容量信息"),
    );
    resultPanel.hidden = false;
  } catch (error) {
    status.textContent = `通讯录检测失败：${error.message}`;
  } finally {
    button.disabled = false;
  }
}

function esimEIDRows(value) {
  const eids = value?.chip_info?.eids;
  return Array.isArray(eids) ? eids : [];
}

function renderESIMChip(overview) {
  const panel = $("#esim-chip");
  const chip = overview?.chip_info || {};
  const eids = esimEIDRows(overview);
  if (!chip.sku_name && !chip.serial_number && !chip.firmware && !eids.length) {
    panel.hidden = true;
    panel.replaceChildren();
    return;
  }
  panel.hidden = false;
  panel.replaceChildren(
    diagnosticCard("卡类型", chip.sku_name || "eUICC/eSIM 卡片"),
    diagnosticCard("固件", chip.firmware || "--", chip.serial_number ? `序列号 ${chip.serial_number}` : ""),
    diagnosticCard("EID", eids.map((item) => item.eid).filter(Boolean).join(" · ") || "--"),
  );
}

function renderESIMEIDList(overview) {
  const eids = esimEIDRows(overview);
  if (!eids.length) return [];
  return eids.map((item) => {
    const row = document.createElement("article");
    row.className = "item esim-info-row";
    const name = document.createElement("strong");
    name.textContent = "已识别 eUICC";
    const detail = document.createElement("p");
    detail.textContent = [
      item.eid ? `EID ${item.eid}` : "",
      item.aid ? `AID ${item.aid}` : "",
      item.free_nvram ? `可用空间 ${item.free_nvram}` : "",
      item.firmware ? `固件 ${item.firmware}` : "",
    ].filter(Boolean).join("\n");
    const status = document.createElement("small");
    status.textContent = item.spec || item.spec_guess || "eSIM";
    row.append(name, detail, status);
    return row;
  });
}

function renderESIMEIDPanel(rows) {
  if (!rows.length) return null;
  const panel = document.createElement("details");
  panel.className = "esim-euicc-panel";
  const heading = document.createElement("summary");
  heading.className = "esim-euicc-heading";
  const title = document.createElement("strong");
  title.textContent = "已识别 eUICC";
  const hint = document.createElement("small");
  hint.textContent = rows.length > 1 ? `${rows.length} 张 eSIM 卡片` : "卡片信息";
  heading.append(title, hint);
  panel.append(heading, ...rows);
  return panel;
}

async function loadESIMHealth() {
  if (esimHealthInFlight) return;
  esimHealthInFlight = true;
  const section = $("#esim-runtime-section");
  const panel = $("#esim-runtime");
  section.hidden = false;
  panel.replaceChildren(diagnosticCard("Profile 检查", "正在检测"));
  try {
    const health = await api("/api/esim/health");
    if (health.card_type === "physical_sim") {
      section.hidden = true;
      return;
    }
    if (!health.active_profile) {
      panel.replaceChildren(diagnosticCard("Profile 检查", health.message || "未发现已启用 Profile"));
      return;
    }
    const profile = health.active_profile;
    const signal = Number.isFinite(health.signal_dbm) ? `${health.signal_dbm} dBm` : "--";
    panel.replaceChildren(
      diagnosticCard("当前启用", profileDisplayName(profile), profile.iccid ? `ICCID ${maskIdentifier(profile.iccid)}` : ""),
      diagnosticCard("模块实际卡", health.module_iccid ? maskIdentifier(health.module_iccid) : "--", health.imsi ? `IMSI ${health.imsi}` : ""),
      diagnosticCard("蜂窝注册", health.registration || "未注册", [displayOperatorName(health.operator), health.network_mode].filter(Boolean).join(" · ")),
      diagnosticCard("信号", signal, health.registered ? "模块已接管当前 Profile" : "等待网络注册"),
    );
  } catch (error) {
    panel.replaceChildren(diagnosticCard("Profile 检查", "暂时无法读取", error.message));
  } finally {
    esimHealthInFlight = false;
  }
}

function setESIMHealthPolling(enabled) {
  clearInterval(esimHealthPollTimer);
  esimHealthPollTimer = null;
  if (!enabled) return;
  esimHealthPollTimer = setInterval(() => {
    if ($("#esim").classList.contains("active")) void loadESIMHealth();
  }, 30000);
}

function diagnosticCard(label, value, detail = "") {
  const card = document.createElement("div");
  card.className = "diagnostic-card";
  const span = document.createElement("span");
  span.textContent = label;
  const strong = document.createElement("strong");
  strong.textContent = value || "--";
  card.append(span, strong);
  if (detail) {
    const small = document.createElement("small");
    small.textContent = detail;
    card.append(small);
  }
  return card;
}

function networkPathStep(label, value, detail = "", tone = "") {
  const step = document.createElement("div");
  step.className = `network-path-step ${tone}`.trim();
  const labelNode = document.createElement("span");
  labelNode.textContent = label;
  const valueNode = document.createElement("strong");
  valueNode.textContent = value || "--";
  const detailNode = document.createElement("small");
  detailNode.textContent = detail;
  step.append(labelNode, valueNode, detailNode);
  return step;
}

function networkFact(label, value) {
  const item = document.createElement("div");
  const term = document.createElement("dt");
  term.textContent = label;
  const description = document.createElement("dd");
  description.textContent = value || "--";
  item.append(term, description);
  return item;
}

function renderNetworkCheck(label, result) {
  const list = $("#network-checks");
  list.className = "list";
  const row = document.createElement("article");
  row.className = `item check-item ${result.ok ? "ok" : "bad"}`;
  const name = document.createElement("strong");
  name.textContent = label;
  const detail = document.createElement("p");
  detail.textContent = result.detail || result.summary || "";
  const status = document.createElement("small");
  status.textContent = result.ok ? "通过" : "未通过";
  row.append(name, detail, status);
  const existing = [...list.querySelectorAll(".item")].filter((item) => item.dataset.label !== label);
  row.dataset.label = label;
  list.replaceChildren(row, ...existing);
}

async function runNetworkCheck(label, path, button) {
  button.disabled = true;
  try {
    const result = await api(path, { method: "POST" });
    renderNetworkCheck(label, result);
    notice(result.summary || "检测完成");
  } catch (error) {
    renderNetworkCheck(label, { ok: false, summary: "检测失败", detail: error.message });
    notice(error.message);
  } finally {
    button.disabled = false;
  }
}

async function loadNetwork() {
  const grid = $("#network-grid");
  const ifaceList = $("#network-interfaces");
  $("#network-status").textContent = "正在读取网络诊断...";
  try {
    const diag = await api("/api/network");
    setUSBNetModeSelector(diag.usbnet_mode);
    const active = Array.isArray(diag.active_contexts) ? diag.active_contexts.join(", ") : "";
    const apns = Array.isArray(diag.pdp_contexts)
      ? diag.pdp_contexts.map((ctx) => `${ctx.id}:${ctx.apn}`).join(" · ")
      : "";
    const addresses = Array.isArray(diag.pdp_addresses) ? diag.pdp_addresses.join(" · ") : "";
    const usb = diag.usb_device
      ? `${diag.usb_device.vendor || ""} ${diag.usb_device.product || ""} (${diag.usb_device.vendor_id}:${diag.usb_device.product_id})`
      : "未检测到";
    const route = diag.default_route || {};
    const routeText = route.interface || "未知";
    const path = document.createElement("div");
    path.className = "network-path";
    path.append(
      networkPathStep("蜂窝数据", active ? `已激活 ${active}` : "未激活", addresses || "等待分配蜂窝 IP", active ? "is-good" : "is-warn"),
      networkPathStep("USB 网卡", diag.usb_network_present ? "已识别" : "未识别", "macOS 网络接口", diag.usb_network_present ? "is-good" : "is-bad"),
      networkPathStep("默认出口", routeText, route.gateway ? `网关 ${route.gateway}` : "macOS 当前默认路由", route.interface ? "is-good" : "is-warn"),
    );
    const facts = document.createElement("dl");
    facts.className = "network-facts";
    facts.append(
      networkFact("USBNET", diag.usbnet_mode ?? "未知"),
      networkFact("APN", apns || "无"),
      networkFact("USB 设备", usb),
    );
    grid.className = "network-summary";
    grid.replaceChildren(path, facts);

    const errorText = diag.errors ? ` · 错误：${Object.values(diag.errors).join("；")}` : "";
    $("#network-status").textContent = diag.usb_network_present
      ? `macOS 已识别 USB 网络接口${errorText}`
      : `蜂窝侧可能已通，但 macOS 尚未识别 USB 网卡${errorText}`;

    const interfaces = Array.isArray(diag.mac_interfaces) ? diag.mac_interfaces : [];
    if (!interfaces.length) {
      ifaceList.className = "list empty";
      ifaceList.textContent = "未读取到网络接口";
      return;
    }
    ifaceList.className = "list";
    ifaceList.replaceChildren(...interfaces.map((item) => {
      const row = document.createElement("article");
      row.className = "item";
      const name = document.createElement("strong");
      name.textContent = item.name;
      const detail = document.createElement("p");
      detail.textContent = [item.kind, item.status, item.ipv4].filter(Boolean).join(" · ");
      const status = document.createElement("small");
      status.textContent = item.status === "active" ? "active" : "inactive";
      row.append(name, detail, status);
      return row;
    }));
  } catch (error) {
    $("#network-status").textContent = `读取网络诊断失败：${error.message}`;
    grid.className = "network-summary network-summary-empty";
    grid.textContent = "网络摘要暂不可用";
    ifaceList.className = "list empty";
    ifaceList.textContent = "读取失败";
    notice(error.message);
  }
}

function formatTrafficBytes(value) {
  const bytes = Math.max(0, Number(value || 0));
  const units = ["B", "KB", "MB", "GB", "TB"];
  let amount = bytes;
  let unit = 0;
  while (amount >= 1024 && unit < units.length - 1) {
    amount /= 1024;
    unit += 1;
  }
  const digits = unit === 0 ? 0 : (amount >= 100 ? 0 : amount >= 10 ? 1 : 2);
  return `${amount.toFixed(digits)} ${units[unit]}`;
}

async function loadNetworkTraffic() {
  if (networkTrafficInFlight) return;
  networkTrafficInFlight = true;
  try {
    const sample = await api("/api/network/traffic");
    if (!sample.available) {
      networkTrafficPrevious = null;
      setValue("#traffic-rx-rate", "--", "muted");
      setValue("#traffic-tx-rate", "--", "muted");
      setValue("#traffic-session-rx", "--", "muted");
      setValue("#traffic-session-tx", "--", "muted");
      setValue("#traffic-session-total", "--", "muted");
      return;
    }

    let rxRate = 0;
    let txRate = 0;
    const previous = networkTrafficPrevious;
    if (previous && previous.interface === sample.interface) {
      const elapsed = (Number(sample.sampled_at_ms) - Number(previous.sampled_at_ms)) / 1000;
      if (elapsed > 0) {
        rxRate = Math.max(0, Number(sample.rx_bytes) - Number(previous.rx_bytes)) / elapsed;
        txRate = Math.max(0, Number(sample.tx_bytes) - Number(previous.tx_bytes)) / elapsed;
      }
    }
    networkTrafficPrevious = sample;
    setValue("#traffic-rx-rate", `${formatTrafficBytes(rxRate)}/s`, "neutral");
    setValue("#traffic-tx-rate", `${formatTrafficBytes(txRate)}/s`, "neutral");
    setValue("#traffic-session-rx", formatTrafficBytes(sample.session_rx_bytes), "neutral");
    setValue("#traffic-session-tx", formatTrafficBytes(sample.session_tx_bytes), "neutral");
    setValue("#traffic-session-total", formatTrafficBytes(sample.session_total_bytes), "neutral");
    $("#traffic-session-total").title = "本次启动期间的下载与上传流量之和；关闭 DJ 4G Hub 后清零";
  } catch (error) {
    setValue("#traffic-rx-rate", "--", "muted");
    setValue("#traffic-tx-rate", "--", "muted");
    setValue("#traffic-session-total", "--", "muted");
  } finally {
    networkTrafficInFlight = false;
  }
}

function setNetworkTrafficPolling(enabled) {
  clearInterval(networkTrafficTimer);
  networkTrafficTimer = null;
  if (!enabled) {
    networkTrafficPrevious = null;
    return;
  }
  void loadNetworkTraffic();
  networkTrafficTimer = setInterval(loadNetworkTraffic, 1000);
}

function activityInitials(value) {
  return String(value || "?").trim().slice(0, 2).toUpperCase();
}

function renderNetworkActivityCountdown() {
  const label = $("#activity-updated");
  if (!label) return;
  label.textContent = networkActivityInFlight
    ? "正在刷新…"
    : `${networkActivityCountdown} 秒后刷新`;
}

async function loadNetworkActivity() {
  if (networkActivityInFlight) return;
  networkActivityInFlight = true;
  renderNetworkActivityCountdown();
  const list = $("#activity-list");
  try {
    const snapshot = await api("/api/network/activity");
    if (!snapshot.available) {
      $("#activity-physical").textContent = "未检测到 4G 网卡";
      $("#activity-tunnel").textContent = "--";
      $("#activity-count").textContent = "0 个连接";
      list.className = "activity-list empty";
      list.textContent = "当前没有可展示的 4G 联网活动";
      return;
    }
    $("#activity-physical").textContent = `${snapshot.physical_interface}${snapshot.physical_ipv4 ? ` · ${snapshot.physical_ipv4}` : ""}${snapshot.physical_active ? " · 活跃" : ""}`;
    $("#activity-tunnel").textContent = snapshot.tunnel_interface || "直连";
    const connections = Array.isArray(snapshot.connections) ? snapshot.connections : [];
    $("#activity-count").textContent = `${connections.length} 个连接`;
    if (!connections.length) {
      list.className = "activity-list empty";
      list.textContent = "当前没有活跃的应用连接";
      return;
    }
    list.className = "activity-list";
    list.replaceChildren(...connections.map((connection) => {
      const row = document.createElement("article");
      row.className = "activity-row";
      const app = document.createElement("div");
      app.className = "activity-app";
      const glyph = document.createElement("i");
      glyph.textContent = activityInitials(connection.process);
      const process = document.createElement("strong");
      process.textContent = connection.process || "系统";
      app.append(glyph, process);
      const target = document.createElement("div");
      target.className = "activity-target";
      const host = document.createElement("strong");
      host.textContent = connection.host || connection.ip;
      const detail = document.createElement("small");
      detail.textContent = connection.ip
        ? `${connection.ip}${connection.port ? `:${connection.port}` : ""}`
        : (connection.port ? `端口 ${connection.port}` : "目标主机");
      target.append(host, detail);
      const protocol = document.createElement("span");
      protocol.className = "activity-protocol";
      protocol.textContent = connection.protocol || "IP";
      const bytes = document.createElement("span");
      bytes.className = "activity-bytes";
      bytes.textContent = `↓ ${formatTrafficBytes(connection.rx_bytes)} · ↑ ${formatTrafficBytes(connection.tx_bytes)}`;
      row.append(app, target, protocol, bytes);
      return row;
    }));
  } catch (error) {
    list.className = "activity-list empty";
    list.textContent = `联网活动读取失败：${error.message}`;
  } finally {
    networkActivityInFlight = false;
    networkActivityCountdown = 5;
    renderNetworkActivityCountdown();
  }
}

function setNetworkActivityPolling(enabled) {
  clearInterval(networkActivityTimer);
  networkActivityTimer = null;
  if (!enabled) return;
  networkActivityCountdown = 5;
  void loadNetworkActivity();
  networkActivityTimer = setInterval(() => {
    if (networkActivityInFlight) {
      renderNetworkActivityCountdown();
      return;
    }
    networkActivityCountdown -= 1;
    if (networkActivityCountdown <= 0) {
      void loadNetworkActivity();
      return;
    }
    renderNetworkActivityCountdown();
  }, 1000);
}

async function setUSBNetMode(mode) {
  const label = `模式 ${mode}`;
  const confirmed = await showModal({
    title: `切换到${label}`,
    message: `将写入 usbnet=${mode}，重启模块后生效。`,
    confirmLabel: "继续切换",
  });
  if (!confirmed) return;
  try {
    const result = await api("/api/network/usbnet", {
      method: "POST",
      body: JSON.stringify({ mode }),
    });
    notice(`usbnet 已写入 ${result.mode}，请重启模块`);
    await loadNetwork();
  } catch (error) {
    notice(error.message);
  }
}

async function switchWorkMode(mode, label, button) {
  if (button.getAttribute("aria-pressed") === "true") {
    notice(`当前已是${label}`);
    return;
  }
  const confirmed = await showModal({
    title: `切换到${label}`,
    message: `将写入 usbnet=${mode} 并重启模块，USB 会短暂断开。`,
    confirmLabel: "确认切换",
  });
  if (!confirmed) return;
  const status = $("#workmode-status");
  const buttons = [$("#workmode-sms"), $("#workmode-network")];
  buttons.forEach((item) => { item.disabled = true; });
  status.hidden = false;
  status.textContent = `正在切到${label}...`;
  try {
    const result = await api("/api/network/usbnet", {
      method: "POST",
      body: JSON.stringify({ mode }),
    });
    status.textContent = `usbnet 已写入 ${result.mode}，正在重启模块...`;
    await api("/api/network/reboot-module", { method: "POST" });
    status.textContent = `${label}已写入，等待模块重新枚举后自动刷新。`;
    notice(`${label}切换中`);
    setTimeout(loadStatus, 8000);
    setTimeout(loadNetwork, 12000);
    setTimeout(() => {
      status.textContent = `${label}切换完成后，请确认状态卡和网络诊断。`;
      buttons.forEach((item) => { item.disabled = false; });
    }, 13000);
  } catch (error) {
    status.hidden = false;
    status.textContent = `${label}切换失败：${error.message}`;
    notice(error.message);
    buttons.forEach((item) => { item.disabled = false; });
  }
}

async function rebootModule() {
  const confirmed = await showModal({
    title: "重启模块",
    message: "模块会重新枚举 USB，网页可能短暂断开。",
    confirmLabel: "确认重启",
  });
  if (!confirmed) return;
  try {
    await api("/api/network/reboot-module", { method: "POST" });
    notice("模块正在重启，稍后刷新状态");
    setTimeout(loadStatus, 8000);
    setTimeout(loadNetwork, 12000);
  } catch (error) {
    notice(error.message);
  }
}

async function loadESIM() {
  const list = $("#esim-list");
  const status = $("#esim-status");
  const download = $("#esim-download-section");
  const runtime = $("#esim-runtime-section");
  const profilePanel = $("#esim-profile-panel");
  const phonebook = $("#esim-phonebook-section");
  $("#esim-chip").hidden = true;
  $("#esim-chip").replaceChildren();
  runtime.hidden = true;
  download.hidden = false;
  profilePanel.hidden = false;
  phonebook.hidden = false;
  list.className = "list empty";
  list.textContent = "正在读取 eUICC";
  status.textContent = "正在通过 AT+CCHO/CGLA 读取 eUICC/eSIM 卡片";
  try {
    const overview = await api("/api/esim");
    if (overview.card_type === "physical_sim") {
      status.textContent = overview.message;
      list.textContent = overview.message;
      download.hidden = true;
      profilePanel.hidden = true;
      phonebook.hidden = true;
      setESIMHealthPolling(false);
      return;
    }
    const notesResponse = await api("/api/esim/module-notes");
    const notes = notesResponse.notes || {};
    const profiles = profileRows(overview);
    const eidRows = renderESIMEIDList(overview);
    const eidPanel = renderESIMEIDPanel(eidRows);
    renderESIMChip(overview);
    const profileCount = profiles.length;
    const eidCount = esimEIDRows(overview).length;
    const active = activeProfile(profiles);
    status.textContent = active
      ? `已读取：${eidCount} 个 eUICC，${profileCount} 个 Profile · 当前使用 ${profileDisplayName(active)}`
      : `已读取：${eidCount} 个 eUICC，${profileCount} 个 Profile · 未发现已启用 Profile`;
    if (!profiles.length) {
      if (eidRows.length) {
        list.className = "list";
        list.replaceChildren(eidPanel);
        return;
      }
      list.textContent = "未发现 eUICC/eSIM 卡片参数";
      return;
    }
    list.className = "list";
    const profileItems = profiles.map((profile) => {
      const note = notes[profile.iccid] || {};
      const row = document.createElement("article");
      row.className = `item esim-profile ${profile.state === 1 ? "active" : ""}`;
      const name = document.createElement("strong");
      name.textContent = note.label || profileDisplayName(profile);
      const detail = document.createElement("p");
      detail.textContent = [
        note.label && note.label !== profileDisplayName(profile) ? `卡内名称：${profileDisplayName(profile)}` : "",
        profile.service_provider_name ? `服务商：${profile.service_provider_name}` : "",
        profile.class_text ? `类型：${profile.class_text}` : "",
        note.tags ? `标签：${note.tags}` : "",
      ].filter(Boolean).join("\n");
      const metadata = document.createElement("div");
      metadata.className = "profile-metadata";
      if (note.phone) {
        const phoneRow = document.createElement("div");
        phoneRow.className = "profile-identifier-row";
        const phone = document.createElement("code");
        phone.className = "profile-iccid";
        phone.textContent = `模块号码 ${maskPhoneNumber(note.phone)}`;
        const revealPhone = document.createElement("button");
        revealPhone.className = "secondary compact profile-toggle-button";
        revealPhone.type = "button";
        revealPhone.textContent = "显示";
        revealPhone.addEventListener("click", () => {
          const hidden = revealPhone.textContent === "显示";
          phone.textContent = `模块号码 ${hidden ? note.phone : maskPhoneNumber(note.phone)}`;
          revealPhone.textContent = hidden ? "隐藏" : "显示";
        });
        const copyPhone = document.createElement("button");
        copyPhone.className = "secondary compact profile-copy-button";
        copyPhone.type = "button";
        copyPhone.textContent = "复制号码";
        copyPhone.addEventListener("click", () => copyIdentifier(note.phone, "模块号码"));
        phoneRow.append(phone, revealPhone, copyPhone);
        metadata.append(phoneRow);
      }
      if (profile.iccid) {
        const iccidRow = document.createElement("div");
        iccidRow.className = "profile-identifier-row";
        const iccid = document.createElement("code");
        iccid.className = "profile-iccid";
        iccid.textContent = `ICCID ${maskIdentifier(profile.iccid)}`;
        const reveal = document.createElement("button");
        reveal.className = "secondary compact profile-toggle-button";
        reveal.type = "button";
        reveal.textContent = "显示";
        reveal.addEventListener("click", () => {
          const hidden = reveal.textContent === "显示";
          iccid.textContent = `ICCID ${hidden ? profile.iccid : maskIdentifier(profile.iccid)}`;
          reveal.textContent = hidden ? "隐藏" : "显示";
        });
        const copy = document.createElement("button");
        copy.className = "secondary compact profile-copy-button";
        copy.type = "button";
        copy.textContent = "复制 ICCID";
        copy.addEventListener("click", () => copyIdentifier(profile.iccid, "ICCID"));
        iccidRow.append(iccid, reveal, copy);
        metadata.append(iccidRow);
      }
      const actionBox = document.createElement("div");
      actionBox.className = "profile-actions";
      if (profile.state !== 1) {
        const button = document.createElement("button");
        button.className = "compact";
        button.textContent = "启用";
        button.addEventListener("click", async () => {
          const label = profileDisplayName(profile);
          const confirmed = await showModal({
            title: "启用 Profile",
            message: `确定启用 ${label} 吗？当前正在使用的 eSIM Profile 会被切换。`,
            confirmLabel: "启用",
          });
          if (!confirmed) {
            return;
          }
          button.disabled = true;
          button.textContent = "切换中";
          try {
            const result = await api("/api/esim/switch", {
              method: "POST",
              body: JSON.stringify({ iccid: profile.iccid, aid: profile.aid || "" }),
            });
            if (result.module_reboot_requested) {
              status.textContent = `已切换到 ${label}；模块正在重启，等待新 Profile 接管（约 ${result.reconnect_wait_seconds || 10} 秒）`;
              notice(`已切换 ${label}，模块正在重新读取新卡`);
              setTimeout(async () => {
                await loadESIM();
                await loadStatus();
              }, (result.reconnect_wait_seconds || 10) * 1000);
            } else {
              status.textContent = `Profile 已切换到 ${label}，但模块重启未确认：${result.module_reboot_warning || "请手动重启后再读取号码"}`;
              notice("Profile 已切换，模块重启未确认");
              await loadESIM();
            }
          } catch (error) {
            status.textContent = `切换失败：${error.message}`;
            notice(error.message);
            button.disabled = false;
            button.textContent = "启用";
          }
        });
        actionBox.append(button);
      } else {
        const button = document.createElement("button");
        button.className = "secondary compact";
        button.type = "button";
        button.textContent = "启用";
        button.disabled = true;
        actionBox.append(button);
      }
      const rename = document.createElement("button");
      rename.className = "secondary compact";
      rename.type = "button";
      rename.textContent = "改名";
      rename.addEventListener("click", async () => {
        const values = await showModal({
          title: "修改 Profile 名称",
          message: "名称将写入 eUICC 卡片内部的 Profile nickname。",
          confirmLabel: "保存",
          fields: [{ name: "name", label: "Profile 名称", value: profileDisplayName(profile), required: true }],
        });
        if (!values?.name) return;
        rename.disabled = true;
        try {
          await api("/api/esim/profile", { method: "PATCH", body: JSON.stringify({ iccid: profile.iccid, aid: profile.aid || "", name: values.name }) });
          notice("Profile 名称已修改");
          await loadESIM();
        } catch (error) { notice(error.message); } finally { rename.disabled = false; }
      });
      const localNote = document.createElement("button");
      localNote.className = "secondary compact";
      localNote.type = "button";
      localNote.textContent = "模块资料";
      localNote.addEventListener("click", () => editProfileNote(profile, note));
      const remove = document.createElement("button");
      remove.className = "secondary danger compact";
      remove.type = "button";
      remove.textContent = "删除";
      remove.disabled = profile.state === 1;
      remove.addEventListener("click", async () => {
        const last4 = String(profile.iccid || "").slice(-4);
        const values = await showModal({
          title: "删除 Profile",
          message: `删除不可恢复。请输入 ICCID 后四位 ${last4} 确认。`,
          confirmLabel: "删除",
          danger: true,
          fields: [{ name: "confirmation", label: "ICCID 后四位", required: true }],
        });
        if (!values) return;
        if (values.confirmation !== last4) {
          notice("ICCID 后四位不匹配，未执行删除");
          return;
        }
        remove.disabled = true;
        try {
          await api("/api/esim/profile", { method: "DELETE", body: JSON.stringify({ iccid: profile.iccid, aid: profile.aid || "" }) });
          notice("Profile 已删除");
          await loadESIM();
        } catch (error) { notice(error.message); } finally { remove.disabled = false; }
      });
      actionBox.append(localNote, rename, remove);
      const description = document.createElement("div");
      description.className = "profile-description";
      description.append(detail, metadata);
      row.append(name, description, actionBox);
      return row;
    });
    list.replaceChildren(...(eidPanel ? [eidPanel] : []), ...profileItems);
    void loadESIMHealth();
    setESIMHealthPolling(true);
  } catch (error) {
    status.textContent = `读取失败：${error.message}`;
    list.textContent = error.message;
    setESIMHealthPolling(false);
  }
}

let sipTimer = null;
let sipSettingsLoaded = false;
let sipInFlight = false;
let alertCall = null;
let callActionPending = false;
let callMuted = false;
let callAudio = null;
let callRingTimer = null;
let callNotification = null;
let callRestoreFocus = null;
let nativeAlertCallID = "";
const callToneNodes = new Set();
const notifiedCalls = new Set();
const normalPageTitle = document.title;

async function unlockCallAudio() {
  const Audio = window.AudioContext || window.webkitAudioContext;
  if (!Audio) return;
  if (!callAudio) callAudio = new Audio();
  if (callAudio.state === "suspended") await callAudio.resume();
}

// Browsers require a user gesture before playing audio.
document.addEventListener("pointerdown", () => { void unlockCallAudio().catch(() => {}); }, { once: true });
document.addEventListener("keydown", () => { void unlockCallAudio().catch(() => {}); }, { once: true });

function playCallRing() {
  if (!alertCall || alertCall.state !== "ringing" || nativeAlertCallID === alertCall.id || callMuted || callAudio?.state !== "running") return;
  for (const offset of [0, 0.45]) {
    const at = callAudio.currentTime + offset;
    const gain = callAudio.createGain();
    gain.gain.setValueAtTime(0, at);
    gain.gain.linearRampToValueAtTime(0.09, at + 0.02);
    gain.gain.setValueAtTime(0.09, at + 0.22);
    gain.gain.linearRampToValueAtTime(0, at + 0.26);
    gain.connect(callAudio.destination);
    let remaining = 2;
    for (const frequency of [440, 480]) {
      const tone = callAudio.createOscillator();
      tone.frequency.value = frequency;
      tone.connect(gain);
      callToneNodes.add(tone);
      tone.onended = () => {
        tone.disconnect();
        callToneNodes.delete(tone);
        if (--remaining === 0) gain.disconnect();
      };
      tone.start(at);
      tone.stop(at + 0.28);
    }
  }
}

function stopCallRing() {
  clearInterval(callRingTimer);
  callRingTimer = null;
  for (const tone of callToneNodes) {
    try { tone.stop(); } catch (_) {}
    tone.disconnect();
  }
  callToneNodes.clear();
  callNotification?.close();
  callNotification = null;
  document.title = normalPageTitle;
}

function updateIncomingCall(status) {
  if ("native_alert_call_id" in status) nativeAlertCallID = status.native_alert_call_id;
  const dialog = $("#incoming-call");
  const call = (status.calls || []).find((c) => c.direction === "inbound" && c.state === "ringing")
    || (status.calls || []).find((c) => c.direction === "inbound" && c.state === "active");
  if (!call) {
    stopCallRing();
    alertCall = null;
    if (dialog.open) {
      dialog.close();
      if (callRestoreFocus?.isConnected) callRestoreFocus.focus();
    }
    return;
  }
  if (alertCall?.id !== call.id) {
    stopCallRing();
    callMuted = false;
    $("#incoming-call-error").hidden = true;
    callRestoreFocus = document.activeElement;
  }
  alertCall = call;
  dialog.dataset.state = call.state;
  $("#incoming-call-number").textContent = call.number && call.number !== "unknown" ? call.number : "未知号码";
  $("#incoming-call-title").textContent = call.state === "ringing" ? "电话来电" : "语音通话";
  $("#incoming-call-state").textContent = call.state === "ringing"
    ? "邀请你进行语音通话"
    : status.audio?.local?.state === "running" || !call.local_audio ? "通话中" : "正在连接声音…";
  $("#incoming-call-answer").hidden = call.state !== "ringing";
  $("#incoming-call-answer").disabled = callActionPending;
  $("#incoming-call-reject").disabled = callActionPending;
  $("#incoming-call-reject span").textContent = call.state === "ringing" ? "拒接" : "挂断";
  $("#incoming-call-reject").setAttribute("aria-label", call.state === "ringing" ? "拒接来电" : "挂断通话");
  const nativeRinging = nativeAlertCallID === call.id;
  $("#incoming-call-mute").hidden = call.state !== "ringing" || nativeRinging;
  const muteLabel = callMuted ? "取消静音" : callAudio?.state === "running" ? "静音铃声" : "开启铃声";
  $("#incoming-call-mute").textContent = muteLabel;
  $("#incoming-call-mute").setAttribute("aria-label", muteLabel);
  if (!dialog.open) {
    dialog.showModal();
    (call.state === "ringing" ? $("#incoming-call-answer") : $("#incoming-call-reject")).focus();
  }
  if (call.state !== "ringing") {
    stopCallRing();
    return;
  }
  if (nativeRinging) {
    stopCallRing();
    document.title = `来电：${$("#incoming-call-number").textContent} · ${normalPageTitle}`;
    return;
  }
  if (!callRingTimer) {
    playCallRing();
    callRingTimer = setInterval(playCallRing, 1800);
  }
  document.title = `来电：${$("#incoming-call-number").textContent} · ${normalPageTitle}`;
  if (document.hidden && "Notification" in window && Notification.permission === "granted" && !notifiedCalls.has(call.id)) {
    notifiedCalls.add(call.id);
    if (notifiedCalls.size > 40) notifiedCalls.delete(notifiedCalls.values().next().value);
    callNotification = new Notification("DJ 4G Hub · 电话来电", {
      body: $("#incoming-call-number").textContent, tag: `call-${call.id}`, requireInteraction: true,
    });
    callNotification.onclick = () => { window.focus(); $("#incoming-call-answer").focus(); };
  }
}

$("#incoming-call").addEventListener("cancel", (event) => {
  event.preventDefault();
  $("#incoming-call-mute").click();
});
$("#incoming-call-mute").addEventListener("click", async () => {
  await unlockCallAudio().catch(() => {});
  callMuted = !callMuted;
  if (callAudio?.state === "running" && $("#incoming-call-mute").textContent === "开启铃声") callMuted = false;
  if (callMuted) {
    for (const tone of callToneNodes) { try { tone.stop(); } catch (_) {} }
  } else playCallRing();
  if (alertCall) updateIncomingCall({ calls: [alertCall] });
});
$("#call-alerts-enable").addEventListener("click", async () => {
  await unlockCallAudio().catch(() => {});
  if ("Notification" in window && Notification.permission === "default") await Notification.requestPermission();
  $("#call-alerts-enable").textContent = "来电提醒已开启";
  notice("Notification" in window && Notification.permission === "granted" ? "铃声和系统来电提醒已开启" : "页面来电弹窗和铃声已开启");
});
async function handleIncomingCallAction(action) {
  if (!alertCall || callActionPending) return;
  const id = alertCall.id;
  callActionPending = true;
  $("#incoming-call-answer").disabled = true;
  $("#incoming-call-reject").disabled = true;
  $("#incoming-call-error").hidden = true;
  try {
    await api(`/api/sip/${action}`, { method: "POST", body: JSON.stringify({ call_id: id }) });
    stopCallRing();
    $("#incoming-call-state").textContent = action === "answer" ? "正在连接声音…" : "正在挂断…";
  } catch (error) {
    $("#incoming-call-error").textContent = error.message;
    $("#incoming-call-error").hidden = false;
  } finally {
    callActionPending = false;
    await loadSIP();
  }
}
$("#incoming-call-answer").addEventListener("click", () => void handleIncomingCallAction("answer"));
$("#incoming-call-reject").addEventListener("click", () => void handleIncomingCallAction("hangup"));

function sipStateLabel(state) {
  return { dialing: "拨号中", ringing: "振铃中", active: "通话中", ended: "已结束", failed: "失败", missed: "未接" }[state] || state;
}

async function loadSIP() {
  if (sipInFlight) return;
  sipInFlight = true;
  const statusLine = $("#sip-status-line");
  try {
    const status = await api("/api/sip/status");
    updateIncomingCall(status);
    $("#sip-listen-state").textContent = status.listening ? "监听中" : "未监听";
    const audio = status.audio || {};
    statusLine.textContent = status.listening
      ? `SIP ${status.listen_addr} · realm ${status.realm} · 用户 ${status.username} · 音频：${audio.detail || "未知"}`
      : `SIP 未监听：${status.listen_error || "已停用"}`;
    const calls = (status.calls || []).slice().sort((a, b) => new Date(a.started_at) - new Date(b.started_at));
    const callsList = $("#sip-calls-list");
    const callsStatus = $("#sip-calls-status");
    if (calls.length === 0) {
      callsList.textContent = "暂无通话";
      callsList.classList.add("empty");
      callsStatus.textContent = "暂无通话";
    } else {
      callsList.classList.remove("empty");
      callsList.innerHTML = "";
      const latest = calls[calls.length - 1];
      callsStatus.textContent = `最近：${latest.direction === "inbound" ? "来电" : "去电"} ${latest.number} · ${sipStateLabel(latest.state)}`;
      $("#sip-call-status").textContent = ["dialing", "ringing", "active"].includes(latest.state)
        ? `${latest.direction === "inbound" ? "来电" : "去电"} ${latest.number} · ${sipStateLabel(latest.state)}`
        : "未通话";
      for (const call of calls.slice().reverse()) {
        const item = document.createElement("div");
        item.className = "item";
        const rtp = call.rtp ? ` · RTP ${call.rtp.local_port} 收 ${call.rtp.rx_packets} 发 ${call.rtp.tx_packets}` : "";
        item.textContent = `${call.direction === "inbound" ? "来电" : "去电"} ${call.number} · ${sipStateLabel(call.state)}${call.error ? " · " + call.error : ""}${rtp}`;
        callsList.appendChild(item);
      }
    }
    const regs = status.registrations || [];
    const regList = $("#sip-reg-list");
    if (regs.length === 0) {
      regList.textContent = "无注册客户端";
      regList.classList.add("empty");
      $("#sip-reg-status").textContent = "无注册";
    } else {
      regList.classList.remove("empty");
      regList.innerHTML = "";
      $("#sip-reg-status").textContent = `${regs.length} 个客户端已注册`;
      for (const reg of regs) {
        const item = document.createElement("div");
        item.className = "item";
        item.textContent = `${reg.username} · ${reg.contact} · ${reg.source}`;
        regList.appendChild(item);
      }
    }
    if (!sipSettingsLoaded) {
      sipSettingsLoaded = true;
      try {
        const settings = await api("/api/sip/settings");
        $("#sip-enabled").value = String(settings.enabled);
        $("#sip-listen").value = settings.listen || "";
        $("#sip-realm").value = settings.realm || "";
        $("#sip-username").value = settings.username || "";
        $("#sip-rtp-min").value = settings.rtp_min || "";
        $("#sip-rtp-max").value = settings.rtp_max || "";
      } catch (_) {}
    }
  } catch (error) {
    statusLine.textContent = `SIP 状态读取失败：${error.message}`;
    if (alertCall) {
      stopCallRing();
      $("#incoming-call-state").textContent = "连接中断，正在重试…";
      $("#incoming-call-answer").disabled = true;
      $("#incoming-call-reject").disabled = true;
    }
  } finally {
    sipInFlight = false;
  }
}

function setSIPPolling() {
  if (sipTimer) return;
  void loadSIP();
  sipTimer = setInterval(() => void loadSIP(), 1000);
}

$("#sip-call-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const input = $("#sip-number");
  const number = input.value.trim();
  if (!number) return;
  const status = $("#sip-call-status");
  status.textContent = `正在拨打 ${number}…`;
  try {
    await api("/api/sip/call", { method: "POST", body: JSON.stringify({ number }) });
    notice(`已发起呼叫：${number}`);
    await loadSIP();
  } catch (error) {
    status.textContent = `拨打失败：${error.message}`;
    notice(error.message);
  }
});

$("#sip-hangup").addEventListener("click", async () => {
  try {
    await api("/api/sip/hangup", { method: "POST", body: JSON.stringify({}) });
    notice("已挂断");
    await loadSIP();
  } catch (error) {
    notice(error.message);
  }
});

$("#sip-answer").addEventListener("click", async () => {
  try {
    await api("/api/sip/answer", { method: "POST", body: JSON.stringify({}) });
    notice("已接听");
    await loadSIP();
  } catch (error) {
    notice(error.message);
  }
});

$("#refresh-sip").addEventListener("click", loadSIP);

$("#sip-settings-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const status = $("#sip-settings-status");
  status.textContent = "正在保存…";
  const body = {
    enabled: $("#sip-enabled").value === "true",
    listen: $("#sip-listen").value.trim(),
    realm: $("#sip-realm").value.trim(),
    username: $("#sip-username").value.trim(),
    rtp_min: Number($("#sip-rtp-min").value) || undefined,
    rtp_max: Number($("#sip-rtp-max").value) || undefined,
  };
  const password = $("#sip-password").value;
  if (password) body.password = password;
  try {
    await api("/api/sip/settings", { method: "POST", body: JSON.stringify(body) });
    status.textContent = "SIP 设置已保存";
    $("#sip-password").value = "";
    notice("SIP 设置已保存");
    await loadSIP();
  } catch (error) {
    status.textContent = `保存失败：${error.message}`;
    notice(error.message);
  }
});

document.querySelectorAll(".tab").forEach((tab) => {
  tab.addEventListener("click", () => {
    document.querySelectorAll(".tab").forEach((item) => {
      item.classList.remove("active");
      item.removeAttribute("aria-current");
    });
    document.querySelectorAll(".view").forEach((view) => view.classList.remove("active"));
    tab.classList.add("active");
    tab.setAttribute("aria-current", "page");
    $(`#${tab.dataset.view}`).classList.add("active");
    setNetworkActivityPolling(tab.dataset.view === "overview");
    if (tab.dataset.view === "esim") loadESIM();
    else setESIMHealthPolling(false);
    if (tab.dataset.view === "calls") void loadSIP();
    if (tab.dataset.view === "tasks") void loadTasks();
    if (tab.dataset.view === "notify") void loadNotify();
    if (tab.dataset.view === "radio") setRadioPolling(true);
    else setRadioPolling(false);
    if (tab.dataset.view === "network") loadNetwork();
    if (tab.dataset.view === "at") {
      requestAnimationFrame(() => {
        const input = $("#at-command");
        input.focus();
        input.setSelectionRange(input.value.length, input.value.length);
      });
    }
  });
});

$("#esim-download-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const confirmed = await showModal({
    title: "下载新的 Profile",
    message: "将向 SM-DP+ 服务器下载并写入新的 eSIM Profile。写入期间请勿拔出模块。",
    confirmLabel: "开始下载",
  });
  if (!confirmed) return;
  const button = event.currentTarget.querySelector("button[type=submit]");
  const status = $("#esim-download-status");
  button.disabled = true;
  status.textContent = "正在下载并写入 Profile，请勿拔出模块...";
  try {
    const result = await api("/api/esim/download", { method: "POST", body: JSON.stringify({
      smdp: $("#esim-smdp").value, matching_id: $("#esim-matching-id").value,
      confirmation_code: $("#esim-confirmation-code").value, imei: $("#esim-imei").value, aid: $("#esim-aid").value,
    }) });
    status.textContent = result.message || "Profile 下载完成，正在重新读取卡片";
    notice("Profile 下载完成");
    await loadESIM();
  } catch (error) { status.textContent = `下载失败：${error.message}`; notice(error.message); } finally { button.disabled = false; }
});

const messageInput = $("#message");
const messageCounter = $("#message-counter");
const updateMessageCounter = () => {
  messageCounter.textContent = `${messageInput.value.length} 字 · 自动分片`;
};
messageInput.addEventListener("input", updateMessageCounter);
updateMessageCounter();

$("#send-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const button = event.submitter;
  const originalLabel = button.textContent;
  button.disabled = true;
  button.textContent = "发送中";
  try {
    const result = await api("/api/sms/send", {
      method: "POST",
      body: JSON.stringify({ phone: $("#phone").value, message: $("#message").value }),
    });
    messageInput.value = "";
    updateMessageCounter();
    const segments = Number(result.segments || 1);
    notice(segments > 1 ? `短信已发送（${segments} 个分片）` : "短信已发送");
  } catch (error) {
    notice(error.message);
  } finally {
    button.disabled = false;
    button.textContent = originalLabel;
  }
});

$("#at-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const output = $("#at-output");
  const input = $("#at-command");
  const command = input.value.trim();
  if (!command) {
    input.value = "";
    input.focus();
    return;
  }
  output.textContent = `› ${command}\n\n执行中...`;
  input.value = "";
  input.focus();
  try {
    const result = await api("/api/at", {
      method: "POST",
      body: JSON.stringify({ command }),
    });
    output.textContent = `› ${command}\n\n${result.response || "OK"}`;
  } catch (error) {
    output.textContent = `› ${command}\n\n${error.message}`;
  }
});

$("#refresh").addEventListener("click", async () => {
  await Promise.all([loadStatus(), loadSMS(), loadSidebarConnection()]);
  notice("状态已刷新");
});
$("#refresh-sms").addEventListener("click", async () => {
  const button = $("#refresh-sms");
  button.disabled = true;
  $("#sms-status").textContent = "正在读取短信...";
  try {
    const result = await api("/api/sms/refresh", { method: "POST" });
    await loadSMS();
    $("#sms-status").textContent = `短信读取完成：${result.count ?? "未知"} 条`;
    notice("短信读取完成");
  } catch (error) {
    $("#sms-status").textContent = `读取短信失败：${error.message}`;
    notice(error.message);
  } finally {
    button.disabled = false;
  }
});
$("#clear-module-sms").addEventListener("click", async () => {
  const confirmed = await showModal({
    title: "清空模块旧短信",
    message: "只会清空模块内部 ME 存储里的旧短信，不会删除 SIM 卡短信。",
    confirmLabel: "确认清空",
    danger: true,
  });
  if (!confirmed) return;
  const button = $("#clear-module-sms");
  button.disabled = true;
  $("#sms-status").textContent = "正在清空模块内部旧短信...";
  try {
    const result = await api("/api/sms/clear-module", { method: "POST" });
    $("#sms-status").textContent = `模块旧短信已清理：${result.before ?? 0} -> ${result.after ?? 0} 条`;
    await loadSMS();
    notice("模块旧短信已清理");
  } catch (error) {
    $("#sms-status").textContent = `清理模块旧短信失败：${error.message}`;
    notice(error.message);
  } finally {
    button.disabled = false;
  }
});
$("#refresh-esim").addEventListener("click", loadESIM);
$("#probe-esim-phonebook").addEventListener("click", probeESIMPhonebook);
$("#refresh-network").addEventListener("click", loadNetwork);
$("#workmode-sms").addEventListener("click", () =>
  switchWorkMode(0, "短信模式", $("#workmode-sms")));
$("#workmode-network").addEventListener("click", () =>
  switchWorkMode(1, "上网模式", $("#workmode-network")));
$("#check-4g-route").addEventListener("click", () =>
  runNetworkCheck("4G 出口", "/api/network/check-4g", $("#check-4g-route")));
$("#check-proxy-route").addEventListener("click", () =>
  runNetworkCheck("代理", "/api/network/check-proxy", $("#check-proxy-route")));
$("#usbnet-mode-0").addEventListener("click", () => setUSBNetMode(0));
$("#usbnet-mode-1").addEventListener("click", () => setUSBNetMode(1));
$("#usbnet-mode-2").addEventListener("click", () => setUSBNetMode(2));
$("#usbnet-mode-3").addEventListener("click", () => setUSBNetMode(3));
$("#reboot-module").addEventListener("click", rebootModule);

// --- USSD 与无线 (Item 2 + 3) -------------------------------------------
let radioTimer = null;

function setRadioPolling(enabled) {
  clearInterval(radioTimer);
  radioTimer = null;
  if (!enabled) return;
  void loadRadio();
  radioTimer = setInterval(() => {
    if ($("#radio").classList.contains("active")) void loadRadio();
  }, 5000);
}

function ussdStateLabel(state) {
  return { active: "等待回复", ended: "已结束", cancelled: "已取消", error: "出错", idle: "无会话" }[state] || state;
}

function deliveryStateLabel(state) {
  return { sent: "已发送 · 等待回执", delivered: "已送达", pending: "投递中", failed: "投递失败", unknown: "未知" }[state] || state;
}

function renderUSSDSession(session) {
  const status = $("#ussd-status");
  const result = $("#ussd-result");
  const replyForm = $("#ussd-reply-form");
  if (!session) {
    status.textContent = "无会话";
    result.textContent = "暂无 USSD 返回";
    replyForm.hidden = true;
    return;
  }
  status.textContent = `${ussdStateLabel(session.state)} · ${session.code} · 通道 ${session.path || "at-cusd"}`;
  result.textContent = session.text || session.error || "（无文本返回）";
  replyForm.hidden = session.state !== "active";
}

async function loadUSSD() {
  try {
    const data = await api("/api/ussd/status");
    renderUSSDSession(data.session || null);
  } catch (error) {
    $("#ussd-status").textContent = `读取失败：${error.message}`;
  }
  try {
    const ims = await api("/api/ussd/ims");
    $("#ussd-ims-line").textContent = ims.ims_state === "unknown" || ims.ims_error
      ? "IMS 状态未知：当前固件未返回有效状态。"
      : ims.ims_registered
      ? "IMS 已注册，但本机构无 IMS USSI 栈，USSD 始终走 AT+CUSD。"
      : "IMS 未注册。IMS USSI（TS 24.390）本机构不支持，USSD 走 AT+CUSD。";
  } catch (error) {
    $("#ussd-ims-line").textContent = `IMS 状态读取失败：${error.message}`;
  }
}

async function loadDelivery() {
  const list = $("#delivery-list");
  try {
    const items = await api("/api/sms/delivery");
    if (!items.length) {
      list.textContent = "暂无送达记录";
      list.classList.add("empty");
      return;
    }
    list.classList.remove("empty");
    list.replaceChildren(...items.map((item) => {
      const row = document.createElement("div");
      row.className = "list-row";
      const when = item.updated_at ? new Date(item.updated_at).toLocaleString() : "";
      row.textContent = `MR ${item.message_ref} · ${item.to} · ${deliveryStateLabel(item.state)}${item.status_text ? " · " + item.status_text : ""} · ${when}`;
      return row;
    }));
  } catch (error) {
    list.textContent = `读取失败：${error.message}`;
  }
}

async function loadServingCell() {
  const el = $("#serving-cell");
  try {
    const cell = await api("/api/radio/cell");
    el.classList.remove("empty");
    el.textContent = `EARFCN ${cell.earfcn} · PCI ${cell.pci} · RSRP ${cell.rsrp} dBm · RSRQ ${cell.rsrq} dB · SINR ${cell.sinr} dB · ${cell.band || cell.rat}`;
  } catch (error) {
    el.textContent = `读取失败：${error.message}`;
  }
}

async function loadOperators() {
  const status = $("#operator-scan-status");
  const list = $("#operator-list");
  try {
    const data = await api("/api/radio/operators");
    if (data.state === "idle") {
      status.textContent = "尚未扫描运营商";
      list.textContent = "暂无扫描结果";
      list.classList.add("empty");
      return;
    }
    if (data.state === "scanning") {
      status.textContent = "正在扫描运营商（30–120 秒），请稍候…";
      return;
    }
    if (data.state === "error") {
      status.textContent = `扫描失败：${data.error || "未知错误"}`;
      return;
    }
    const operators = data.operators || [];
    status.textContent = operators.length ? `扫描完成：${operators.length} 个运营商` : "扫描完成，未发现运营商";
    if (!operators.length) {
      list.textContent = "暂无扫描结果";
      list.classList.add("empty");
      return;
    }
    list.classList.remove("empty");
    list.replaceChildren(...operators.map((op) => {
      const row = document.createElement("button");
      row.type = "button";
      row.className = "list-row operator-row";
      row.textContent = `${op.operator_name || op.plmn} · PLMN ${op.plmn} · ${op.status}${op.rats && op.rats.length ? " · " + op.rats.join("/") : ""}`;
      row.addEventListener("click", () => {
        $("#operator-mode").value = "manual";
        $("#operator-plmn").value = op.plmn;
        if (op.rats && op.rats.length) $("#operator-rat").value = op.rats[0];
        notice(`已选择 ${op.operator_name || op.plmn}，点“应用”切换`);
      });
      return row;
    }));
  } catch (error) {
    status.textContent = `读取失败：${error.message}`;
  }
}

async function loadCellLock() {
  const status = $("#cell-lock-status");
  const saved = $("#cell-lock-saved");
  try {
    const data = await api("/api/radio/cell-lock");
    status.textContent = data.supported === false
      ? "当前固件无法查询小区锁定状态"
      : data.locked
      ? `已锁定：EARFCN ${data.earfcn} / PCI ${data.pci}`
      : "未锁定（自动选小区）";
    if (data.saved && data.saved.cell_lock) {
      const pref = data.saved.cell_lock;
      saved.textContent = pref.locked
        ? `已保存偏好（ICCID ${data.iccid || "未知"}）：锁定 EARFCN ${pref.earfcn} / PCI ${pref.pci}`
        : "已保存偏好：不锁定";
    } else {
      saved.textContent = "";
    }
  } catch (error) {
    status.textContent = `读取失败：${error.message}`;
  }
}

async function loadOperatorCurrent() {
  try {
    const data = await api("/api/radio/operator");
    const current = data.current || {};
    $("#operator-status").textContent = current.mode === "manual"
      ? `当前：手动 ${current.plmn || ""}`
      : "当前：自动选择";
  } catch (error) {
    $("#operator-status").textContent = `读取失败：${error.message}`;
  }
}

async function loadRadio() {
  const status = $("#radio-status");
  status.textContent = "正在读取…";
  await Promise.all([loadUSSD(), loadDelivery(), loadServingCell(), loadOperators(), loadCellLock(), loadOperatorCurrent(), loadGNSS(), loadModules()]);
  status.textContent = `已更新 ${new Date().toLocaleTimeString()}`;
}

$("#ussd-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const code = $("#ussd-code").value.trim();
  const via = $("#ussd-via").value;
  const status = $("#ussd-status");
  status.textContent = "正在发送 USSD…";
  try {
    const data = await api("/api/ussd", { method: "POST", body: JSON.stringify({ code, via }) });
    renderUSSDSession(data.session);
    if (data.ims_note) notice(data.ims_note);
  } catch (error) {
    status.textContent = `发送失败：${error.message}`;
    notice(error.message);
    await loadUSSD();
  }
});

$("#ussd-reply-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const response = $("#ussd-reply").value.trim();
  const status = $("#ussd-status");
  status.textContent = "正在回复…";
  try {
    const data = await api("/api/ussd/continue", { method: "POST", body: JSON.stringify({ response }) });
    renderUSSDSession(data.session);
    $("#ussd-reply").value = "";
  } catch (error) {
    status.textContent = `回复失败：${error.message}`;
    notice(error.message);
    await loadUSSD();
  }
});

$("#ussd-cancel").addEventListener("click", async () => {
  try {
    const data = await api("/api/ussd/cancel", { method: "POST", body: "{}" });
    renderUSSDSession(data.session);
    notice("USSD 会话已取消");
  } catch (error) {
    notice(error.message);
  }
});

$("#refresh-radio").addEventListener("click", () => void loadRadio());
$("#refresh-delivery").addEventListener("click", () => void loadDelivery());
$("#refresh-cell").addEventListener("click", () => void loadServingCell());

$("#scan-operators").addEventListener("click", async () => {
  $("#operator-scan-status").textContent = "正在启动扫描…";
  try {
    await api("/api/radio/operators/scan", { method: "POST", body: "{}" });
    await loadOperators();
  } catch (error) {
    $("#operator-scan-status").textContent = `扫描启动失败：${error.message}`;
    notice(error.message);
  }
});

$("#operator-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const mode = $("#operator-mode").value;
  const plmn = $("#operator-plmn").value.trim();
  const rat = $("#operator-rat").value;
  const status = $("#operator-status");
  if (mode === "manual") {
    const confirmed = await showModal({
      title: "切换运营商",
      message: `手动切换到 PLMN ${plmn || "（未填）"} 会使模块重新注册，数据连接可能中断约一分钟。确定继续吗？`,
      confirmLabel: "确定切换",
    });
    if (!confirmed) return;
  }
  status.textContent = "正在切换运营商…";
  try {
    const data = await api("/api/radio/operator", { method: "POST", body: JSON.stringify({ mode, plmn, rat }) });
    status.textContent = `已切换：${data.selection.mode === "manual" ? "手动 " + (data.selection.plmn || "") : "自动选择"}`;
    notice("运营商已切换");
    await loadOperators();
  } catch (error) {
    status.textContent = `切换失败：${error.message}`;
    notice(error.message);
  }
});

$("#cell-lock-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const earfcn = Number($("#cell-earfcn").value);
  const pci = Number($("#cell-pci").value);
  const confirmed = await showModal({
    title: "锁定小区",
    message: `锁定到 EARFCN ${earfcn} / PCI ${pci} 后，模块只驻留该小区；重启模块后锁定失效。确定继续吗？`,
    confirmLabel: "确定锁定",
  });
  if (!confirmed) return;
  const status = $("#cell-lock-status");
  status.textContent = "正在锁定小区…";
  try {
    const data = await api("/api/radio/cell-lock", { method: "POST", body: JSON.stringify({ earfcn, pci }) });
    status.textContent = `已锁定：EARFCN ${data.earfcn} / PCI ${data.pci}${data.verified ? "（已向模块确认）" : "（模块已接受，未能读回确认）"}`;
    notice("小区已锁定");
    await loadCellLock();
  } catch (error) {
    status.textContent = `锁定失败：${error.message}`;
    notice(error.message);
  }
});

$("#cell-unlock").addEventListener("click", async () => {
  const status = $("#cell-lock-status");
  status.textContent = "正在解除锁定…";
  try {
    await api("/api/radio/cell-unlock", { method: "POST", body: "{}" });
    status.textContent = "未锁定（自动选小区）";
    notice("小区锁定已解除");
    await loadCellLock();
  } catch (error) {
    status.textContent = `解除失败：${error.message}`;
    notice(error.message);
  }
});

// --- GNSS 与模块 (Item 6) -----------------------------------------------
async function loadGNSS() {
  const line = $("#gnss-line");
  const detail = $("#gnss-detail");
  const toggle = $("#gnss-toggle");
  try {
    const st = await api("/api/gnss");
    toggle.textContent = st.enabled ? "关闭定位" : "开启定位";
    toggle.dataset.enabled = String(!!st.enabled);
    if (st.fix && st.latitude != null && st.longitude != null) {
      line.textContent = `已定位：${st.latitude.toFixed(5)}, ${st.longitude.toFixed(5)}${st.satellites != null ? ` · ${st.satellites} 颗星` : ""}`;
      detail.classList.remove("empty");
      detail.textContent = `纬度 ${st.latitude}，经度 ${st.longitude}，海拔 ${st.altitude_m ?? "--"} 米，HDOP ${st.hdop ?? "--"}，速度 ${st.speed_kmh ?? "--"} km/h，UTC ${st.utc_time || "--"} ${st.date || ""}`;
    } else {
      line.textContent = st.enabled ? "定位已开启，等待卫星信号…" : "定位未开启";
      detail.classList.add("empty");
      detail.textContent = st.note || (st.enabled ? "未定位：需要 GPS 天线与开阔天空" : "点击“开启定位”启动接收机");
    }
  } catch (error) {
    line.textContent = `GNSS 读取失败：${error.message}`;
  }
}

$("#gnss-toggle").addEventListener("click", async () => {
  const toggle = $("#gnss-toggle");
  const enabled = toggle.dataset.enabled !== "true";
  toggle.disabled = true;
  try {
    await api("/api/gnss", { method: "POST", body: JSON.stringify({ enabled }) });
    notice(enabled ? "定位已开启" : "定位已关闭");
    await loadGNSS();
  } catch (error) {
    notice(error.message);
  } finally {
    toggle.disabled = false;
  }
});

async function loadModules() {
  const list = $("#modules-list");
  try {
    const data = await api("/api/modules");
    list.classList.remove("empty");
    list.innerHTML = "";
    if (!data.modules || data.modules.length === 0) {
      list.classList.add("empty");
      list.textContent = "未发现模块";
      return;
    }
    data.modules.forEach((m) => {
      const item = document.createElement("div");
      item.className = "item";
      item.textContent = `${m.product || "模块"} ${m.vendor_id}:${m.product_id} · ${m.location_id}${m.active ? " · 当前模块" : ""}${m.imei ? ` · IMEI ${m.imei}` : ""}${m.iccid ? ` · ICCID ${m.iccid}` : ""}${m.note ? ` · ${m.note}` : ""}`;
      list.appendChild(item);
    });
  } catch (error) {
    list.classList.add("empty");
    list.textContent = `模块读取失败：${error.message}`;
  }
}

$("#refresh-modules").addEventListener("click", () => void loadModules());

// --- 定时任务 (Item 4) + MCP 状态 (Item 7) --------------------------------
function taskScheduleLabel(t) {
  if (t.daily) return `每天 ${t.daily}`;
  if (t.interval_m) return `每 ${t.interval_m} 分钟`;
  return "未排程";
}

function taskKindLabel(kind) {
  return { ussd: "USSD 查询", sms: "发送短信", reboot: "重启模块" }[kind] || kind;
}

function renderTasks(tasks) {
  const list = $("#tasks-list");
  list.innerHTML = "";
  if (!tasks || tasks.length === 0) {
    list.classList.add("empty");
    list.textContent = "暂无任务";
    return;
  }
  list.classList.remove("empty");
  tasks.forEach((t) => {
    const item = document.createElement("div");
    item.className = "item";
    const next = t.next_run_at ? new Date(t.next_run_at).toLocaleString() : "--";
    const last = t.last_run_at ? new Date(t.last_run_at).toLocaleString() : "未运行";
    const target = t.kind === "ussd" ? t.code : t.kind === "sms" ? `${t.to}：${t.text}` : "模块重启";
    item.textContent = `${t.enabled ? "●" : "○"} ${t.name} · ${taskKindLabel(t.kind)} · ${target} · ${taskScheduleLabel(t)} · 下次 ${next} · 上次 ${last}${t.last_result ? ` · ${t.last_result}` : ""} · 共 ${t.run_count || 0} 次`;
    const actions = document.createElement("span");
    actions.style.marginLeft = "8px";
    const toggle = document.createElement("button");
    toggle.className = "secondary";
    toggle.type = "button";
    toggle.textContent = t.enabled ? "停用" : "启用";
    toggle.addEventListener("click", async () => {
      try {
        await api("/api/tasks/enable", { method: "POST", body: JSON.stringify({ id: t.id, enabled: !t.enabled }) });
        await loadTasks();
      } catch (error) { notice(error.message); }
    });
    const run = document.createElement("button");
    run.className = "secondary";
    run.type = "button";
    run.textContent = "立即执行";
    run.addEventListener("click", async () => {
      try {
        await api("/api/tasks/run-now", { method: "POST", body: JSON.stringify({ id: t.id }) });
        notice("任务已启动，稍后刷新查看结果");
        setTimeout(() => void loadTasks(), 3000);
      } catch (error) { notice(error.message); }
    });
    const del = document.createElement("button");
    del.className = "secondary danger";
    del.type = "button";
    del.textContent = "删除";
    del.addEventListener("click", async () => {
      const confirmed = await showModal({ title: "删除任务", message: `确定删除任务“${t.name}”吗？`, confirmLabel: "删除", danger: true });
      if (!confirmed) return;
      try {
        await api("/api/tasks/delete", { method: "POST", body: JSON.stringify({ id: t.id }) });
        await loadTasks();
      } catch (error) { notice(error.message); }
    });
    actions.append(toggle, run, del);
    item.appendChild(actions);
    list.appendChild(item);
  });
}

async function loadTasks() {
  const status = $("#tasks-status");
  status.textContent = "正在读取…";
  try {
    const tasks = await api("/api/tasks");
    renderTasks(tasks);
    status.textContent = `已更新 ${new Date().toLocaleTimeString()} · ${tasks.length} 个任务`;
  } catch (error) {
    status.textContent = `读取失败：${error.message}`;
  }
  try {
    const mcp = await api("/api/mcp/status");
    $("#mcp-line").textContent = mcp.token_configured
      ? "MCP 已启用：POST /api/mcp（Bearer token，见服务端 mcp-token 文件，0600），只读工具集；敏感操作一律 501 拒绝。"
      : "MCP 未启用。";
  } catch (error) {
    $("#mcp-line").textContent = `MCP 状态读取失败：${error.message}`;
  }
}

$("#refresh-tasks").addEventListener("click", () => void loadTasks());

function syncTaskFormFields() {
  const kind = $("#task-kind").value;
  $("#task-code").hidden = kind !== "ussd";
  $("#task-to").hidden = kind !== "sms";
  $("#task-text").hidden = kind !== "sms";
}

$("#task-kind").addEventListener("change", syncTaskFormFields);
syncTaskFormFields();

$("#task-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const status = $("#task-form-status");
  const kind = $("#task-kind").value;
  const body = {
    name: $("#task-name").value.trim(),
    kind,
    code: $("#task-code").value.trim(),
    to: $("#task-to").value.trim(),
    text: $("#task-text").value.trim(),
    interval_m: parseInt($("#task-interval").value, 10) || 0,
    daily: $("#task-daily").value.trim(),
  };
  status.textContent = "正在创建任务…";
  try {
    await api("/api/tasks", { method: "POST", body: JSON.stringify(body) });
    status.textContent = "任务已创建";
    notice("任务已创建");
    event.target.reset();
    syncTaskFormFields();
    await loadTasks();
  } catch (error) {
    status.textContent = `创建失败：${error.message}`;
    notice(error.message);
  }
});

loadStatus();

loadStatus();
loadSMS();
loadSidebarConnection();
setSIPPolling();
setNetworkTrafficPolling(true);
setNetworkActivityPolling(true);
setInterval(loadStatus, 10000);
setInterval(loadSMS, 5000);
setInterval(loadSidebarConnection, 10000);

// --- Notifications (Batch 1) ---------------------------------------------
let notifyView = null;

function notifyField(form, name) {
  const el = form.querySelector(`[name="${name}"]`);
  return el ? el.value.trim() : "";
}

function notifySplitList(value) {
  return value.split(/[,，\n]/).map((s) => s.trim()).filter(Boolean);
}

function fillNotifyForm(channel, view) {
  const form = document.querySelector(`[data-notify-form="${channel}"]`);
  if (!form) return;
  const data = view[channel] || {};
  const enabled = form.querySelector(".notify-enabled");
  if (enabled) enabled.checked = !!data.enabled;
  const set = (name, value) => {
    const el = form.querySelector(`[name="${name}"]`);
    if (el && value !== undefined && value !== null) el.value = value;
  };
  if (channel === "telegram") {
    set("chat_id", data.chat_id || "");
    set("admin_id", data.admin_id || "");
    set("base_url", data.base_url || "");
  } else if (channel === "bark") {
    set("group", data.group || "");
    set("level", data.level || "");
  } else if (channel === "email") {
    set("smtp_host", data.smtp_host || "");
    set("smtp_port", data.smtp_port || "");
    set("username", data.username || "");
    set("from_address", data.from_address || "");
    const useSSL = form.querySelector('[name="use_ssl"]');
    if (useSSL) useSSL.checked = !!data.use_ssl;
  } else if (channel === "pushplus") {
    set("topic", data.topic || "");
    set("channel", data.channel || "");
  } else if (channel === "webhook") {
    set("timeout_ms", data.timeout_ms || "");
    set("retry_max", data.retry_max || "");
  }
}

function renderNotifyLog(entries) {
  const list = $("#notify-log");
  if (!entries || entries.length === 0) {
    list.classList.add("empty");
    list.textContent = "暂无推送记录";
    return;
  }
  list.classList.remove("empty");
  list.innerHTML = "";
  entries.forEach((e) => {
    const item = document.createElement("div");
    item.className = "list-item";
    const ok = e.ok ? "成功" : "失败";
    item.textContent = `${e.time} · ${e.channel} · ${e.kind} · ${ok} · ${e.title}${e.error ? " — " + e.error : ""}`;
    list.appendChild(item);
  });
}

async function loadNotify() {
  const status = $("#notify-status");
  status.textContent = "正在读取…";
  try {
    const view = await api("/api/notify/settings");
    notifyView = view;
    $("#notify-sms").checked = view.notify_sms !== false;
    $("#notify-calls").checked = view.notify_calls !== false;
    ["telegram", "feishu", "bark", "email", "pushplus", "webhook"].forEach((c) => fillNotifyForm(c, view));
    const summary = (view.channels || []).map((c) => `${c.channel}${c.enabled ? "✓" : "✗"}${c.configured ? "" : "(未配置)"}`).join(" · ");
    status.textContent = `已更新 ${new Date().toLocaleTimeString()} · ${summary}`;
    const log = await api("/api/notify/log");
    renderNotifyLog(log.entries);
  } catch (error) {
    status.textContent = `读取失败：${error.message}`;
  }
}

$("#refresh-notify").addEventListener("click", () => void loadNotify());

$("#notify-toggles").addEventListener("submit", async (event) => {
  event.preventDefault();
  const status = $("#notify-toggles-status");
  status.textContent = "正在保存…";
  try {
    await api("/api/notify/settings", {
      method: "POST",
      body: JSON.stringify({ notify_sms: $("#notify-sms").checked, notify_calls: $("#notify-calls").checked }),
    });
    status.textContent = "已保存";
    notice("通知开关已保存");
  } catch (error) {
    status.textContent = `保存失败：${error.message}`;
  }
});

document.querySelectorAll("[data-notify-form]").forEach((form) => {
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const channel = form.dataset.notifyForm;
    const statusEl = form.querySelector(".notify-form-status");
    const enabled = form.querySelector(".notify-enabled")?.checked || false;
    const body = {};
    if (channel === "telegram") {
      body.telegram = {
        enabled,
        bot_token: notifyField(form, "bot_token"),
        chat_id: parseInt(notifyField(form, "chat_id"), 10) || 0,
        admin_id: parseInt(notifyField(form, "admin_id"), 10) || 0,
        base_url: notifyField(form, "base_url"),
      };
    } else if (channel === "feishu") {
      body.feishu = { enabled, webhook_url: notifyField(form, "webhook_url") };
    } else if (channel === "bark") {
      body.bark = {
        enabled,
        urls: notifySplitList(notifyField(form, "urls")),
        group: notifyField(form, "group"),
        level: notifyField(form, "level"),
      };
    } else if (channel === "email") {
      body.email = {
        enabled,
        use_ssl: form.querySelector('[name="use_ssl"]')?.checked || false,
        smtp_host: notifyField(form, "smtp_host"),
        smtp_port: parseInt(notifyField(form, "smtp_port"), 10) || 0,
        username: notifyField(form, "username"),
        password: notifyField(form, "password"),
        from_address: notifyField(form, "from_address"),
        to_addresses: notifySplitList(notifyField(form, "to_addresses")),
      };
    } else if (channel === "pushplus") {
      body.pushplus = {
        enabled,
        token: notifyField(form, "token"),
        topic: notifyField(form, "topic"),
        channel: notifyField(form, "channel"),
      };
    } else if (channel === "webhook") {
      body.webhook = {
        enabled,
        urls: notifySplitList(notifyField(form, "urls")),
        secret: notifyField(form, "secret"),
        timeout_ms: parseInt(notifyField(form, "timeout_ms"), 10) || 0,
        retry_max: parseInt(notifyField(form, "retry_max"), 10) || 0,
      };
    }
    // Only send non-empty fields: the form never shows stored URLs or
    // secrets, so an untouched empty input must not wipe them.
    const ch = body[channel];
    Object.keys(ch).forEach((k) => {
      if (k === "enabled") return;
      const v = ch[k];
      if (v === "" || (Array.isArray(v) && v.length === 0)) delete ch[k];
      if ((k === "timeout_ms" || k === "retry_max") && v === 0) delete ch[k];
    });
    statusEl.textContent = "正在保存…";
    try {
      await api("/api/notify/settings", { method: "POST", body: JSON.stringify(body) });
      statusEl.textContent = "已保存";
      notice(`${channel} 设置已保存`);
      form.querySelectorAll('input[type="password"]').forEach((el) => { el.value = ""; });
      await loadNotify();
    } catch (error) {
      statusEl.textContent = `保存失败：${error.message}`;
    }
  });
});

document.querySelectorAll("[data-notify-test]").forEach((button) => {
  button.addEventListener("click", async () => {
    const channel = button.dataset.notifyTest;
    const original = button.textContent;
    button.textContent = "测试中…";
    button.disabled = true;
    try {
      const result = await api("/api/notify/test", { method: "POST", body: JSON.stringify({ channel }) });
      if (result.ok) {
        notice(`${channel} 测试成功`);
      } else {
        notice(`${channel} 测试未发送：${result.error || "未知原因"}`);
      }
    } catch (error) {
      notice(`${channel} 测试失败：${error.message}`);
    } finally {
      button.textContent = original;
      button.disabled = false;
      await loadNotify();
    }
  });
});
