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
let currentSIMPhoneNumber = "";
let simPhoneNumberRevealed = false;

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
    state.textContent = connection.is_default ? "macOS 出口" : "已连接";
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

function renderSIMPhoneNumber(value, simInserted) {
  const phoneNumber = String(value || "").trim();
  const empty = $("#sim-phone-empty");
  const actions = $("#sim-phone-actions");
  const toggle = $("#sim-phone-toggle");
  const copy = $("#sim-phone-copy");

  if (!phoneNumber) {
    currentSIMPhoneNumber = "";
    simPhoneNumberRevealed = false;
    empty.textContent = simInserted ? "SIM 未存储号码" : "卡片状态";
    empty.hidden = false;
    actions.hidden = true;
    toggle.textContent = "--";
    copy.disabled = true;
    return;
  }

  if (phoneNumber !== currentSIMPhoneNumber) {
    currentSIMPhoneNumber = phoneNumber;
    simPhoneNumberRevealed = false;
  }
  empty.hidden = true;
  actions.hidden = false;
  toggle.textContent = simPhoneNumberRevealed ? phoneNumber : maskPhoneNumber(phoneNumber);
  toggle.title = simPhoneNumberRevealed ? "隐藏本机号码" : "显示完整本机号码";
  copy.disabled = false;
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
    renderSIMPhoneNumber(status.phone_number, status.sim_inserted);
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
    renderSIMPhoneNumber("", false);
    setWorkModeControl(null);
  }
}


// --- Subscription identity & number (delivery point 5) -------------------
// 本机号码只在启动、手动刷新或换卡时读取，不跟随 10 秒状态轮询，避免
// 对模块增加 AT 压力。备注号码明确标注为用户备注，不显示为运营商已验证。
let lastSubscription = null;
let subscriptionNumberRevealed = false;

function renderSubscription(data) {
  lastSubscription = data;
  const number = data.number || "";
  const shown = !number ? "--" : (subscriptionNumberRevealed ? number : maskPhoneNumber(number));
  setValue("#subscription-number", shown, number ? "neutral" : "muted");
  setValue("#subscription-number-source", data.number_label || data.number_source || "--", data.number_source === "remark" ? "warn" : "neutral");
  setValue("#subscription-id", data.subscription_id || "--", "neutral");
  setValue("#subscription-iccid", data.iccid_masked || "--", "neutral");
  const source = $("#subscription-source");
  source.textContent = data.stale
    ? "号码可能过期：上次刷新失败，显示的是旧读数"
    : (data.number_source === "auto" ? "号码由 SIM 自动读取（AT+CNUM）"
      : data.number_source === "remark" ? "当前显示的是用户备注号码，未经运营商验证"
      : "未读到号码：可手动备注，或检查 SIM 卡");
  const remark = $("#subscription-remark");
  if (document.activeElement !== remark) remark.value = "";
}

async function loadSubscription() {
  try {
    renderSubscription(await api("/api/subscription"));
  } catch (error) {
    $("#subscription-source").textContent = `号码未读取：${error.message}`;
  }
}

async function saveSubscriptionRemark(number) {
  const saved = await api("/api/subscription/remark", { method: "POST", body: JSON.stringify({ number }) });
  renderSubscription(saved);
  notice(number ? "备注号码已保存" : "备注已清除");
}

$("#subscription-number").addEventListener("click", () => {
  if (!lastSubscription || !lastSubscription.number) return;
  subscriptionNumberRevealed = !subscriptionNumberRevealed;
  renderSubscription(lastSubscription);
});

$("#refresh-subscription").addEventListener("click", async (event) => {
  const button = event.currentTarget;
  button.disabled = true;
  try {
    renderSubscription(await api("/api/subscription/refresh", { method: "POST" }));
  } catch (error) {
    notice(error.message);
  } finally {
    button.disabled = false;
  }
});

$("#subscription-remark-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  try {
    await saveSubscriptionRemark($("#subscription-remark").value);
  } catch (error) {
    notice(error.message);
  }
});

$("#subscription-remark-clear").addEventListener("click", async () => {
  try {
    await saveSubscriptionRemark("");
  } catch (error) {
    notice(error.message);
  }
});

// --- Init diagnostics (delivery point 5) ---------------------------------
// 只报告结果与建议，不执行任何修复动作；通话期间修复一律视为被阻塞。
async function runInitDiagnostics(button) {
  button.disabled = true;
  try {
    const result = await api("/api/diagnostics/init");
    const summary = $("#diagnostics-summary");
    summary.textContent = result.summary === "ok"
      ? "全部通过：未发现需要恢复的问题"
      : result.summary === "attention" ? "有待观察项，见下方建议"
      : result.summary === "error" ? "发现需要处理的问题，见下方建议"
      : "部分项目未确认";
    if (result.call_active) summary.textContent += "（通话进行中，修复动作已阻塞）";
    const list = $("#diagnostics-list");
    list.classList.remove("empty");
    list.replaceChildren(...result.checks.map((check) => diagnosticCard(
      `${check.label} · ${check.status === "pass" ? "通过" : check.status === "warn" ? "待观察" : check.status === "fail" ? "失败" : "未确认"}`,
      check.detail,
      check.suggestion || "",
    )));
    const note = document.createElement("p");
    note.className = "inline-status";
    note.textContent = result.note || "";
    list.append(note);
  } catch (error) {
    $("#diagnostics-summary").textContent = `诊断失败：${error.message}`;
  } finally {
    button.disabled = false;
  }
}

$("#run-diagnostics").addEventListener("click", (event) => void runInitDiagnostics(event.currentTarget));

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

function showESIMCardState(title, detail, tone = "") {
  const panel = $("#esim-card-state");
  panel.className = `esim-card-state${tone ? ` ${tone}` : ""}`;
  $("#esim-card-state-title").textContent = title;
  $("#esim-card-state-detail").textContent = detail;
  panel.hidden = false;
}

function hideESIMCardState() {
  $("#esim-card-state").hidden = true;
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

function renderNetworkRecovery(diag) {
  const panel = $("#network-recovery");
  const service = diag.network_service;
  if (!service || diag.usb_network_ready) {
    panel.hidden = true;
    return;
  }

  const identity = [service.name, service.device].filter(Boolean).join(" · ");
  const disabled = Boolean(service.disabled);
  $("#network-recovery-title").textContent = disabled
    ? "macOS 已禁用当前 USB 网卡"
    : "USB 网卡尚未取得 DHCP 地址";
  $("#network-recovery-detail").textContent = disabled
    ? `${identity} 已存在，但网络服务处于禁用状态。启用后将重新请求地址。`
    : `${identity} 已启用，但当前没有 IPv4 地址。可以让 macOS 重新请求 DHCP。`;
  $("#enable-network-service").textContent = disabled ? "启用并获取 IP" : "重新请求 DHCP";
  panel.hidden = false;
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
    const service = diag.network_service;
    let usbNetworkValue = "未识别";
    let usbNetworkDetail = "macOS 网络接口";
    let usbNetworkTone = "is-bad";
    if (service?.disabled) {
      usbNetworkValue = "服务已禁用";
      usbNetworkDetail = [service.name, service.device].filter(Boolean).join(" · ");
    } else if (diag.usb_network_ready) {
      usbNetworkValue = service?.device ? `${service.device} 已连接` : "已连接";
      usbNetworkDetail = service?.ipv4 || "已取得 DHCP 地址";
      usbNetworkTone = "is-good";
    } else if (diag.usb_network_present) {
      usbNetworkValue = "等待 DHCP";
      usbNetworkDetail = service?.device || "macOS 已识别接口";
      usbNetworkTone = "is-warn";
    }
    const route = diag.default_route || {};
    const routeText = route.interface || "未知";
    const routeUsesUSB = Boolean(service?.device && route.interface === service.device);
    let routeDetail = route.gateway ? `网关 ${route.gateway}` : "macOS 当前默认路由";
    if (routeUsesUSB) {
      routeDetail += " · 公网尚未验证";
    }
    const path = document.createElement("div");
    path.className = "network-path";
    path.append(
      networkPathStep("蜂窝数据", active ? `已激活 ${active}` : "未激活", addresses || "等待分配蜂窝 IP", active ? "is-good" : "is-warn"),
      networkPathStep("USB 网卡", usbNetworkValue, usbNetworkDetail, usbNetworkTone),
      networkPathStep("macOS 出口", routeText, routeDetail, routeUsesUSB ? "is-good" : "is-warn"),
    );
    const facts = document.createElement("dl");
    facts.className = "network-facts";
    facts.append(
      networkFact("USBNET", diag.usbnet_mode ?? "未知"),
      networkFact("APN", apns || "无"),
      networkFact("macOS 服务", service ? `${service.name} · ${service.disabled ? "已禁用" : "已启用"}` : "未创建"),
      networkFact("USB 设备", usb),
    );
    grid.className = "network-summary";
    grid.replaceChildren(path, facts);

    const errorText = diag.errors ? ` · 错误：${Object.values(diag.errors).join("；")}` : "";
    if (service?.disabled) {
      $("#network-status").textContent = `macOS 网络服务已禁用${errorText}`;
    } else if (diag.usb_network_ready) {
      $("#network-status").textContent = `USB 网卡已连接并取得地址 · 公网尚未验证${errorText}`;
    } else if (diag.usb_network_present) {
      $("#network-status").textContent = `macOS 已识别 USB 网卡，正在等待 DHCP${errorText}`;
    } else {
      $("#network-status").textContent = `蜂窝侧可能已通，但 macOS 尚未识别 USB 网卡${errorText}`;
    }
    renderNetworkRecovery(diag);

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
    $("#network-recovery").hidden = true;
    notice(error.message);
  }
}

async function enableNetworkService() {
  const confirmed = await showModal({
    title: "启用 macOS USB 网卡",
    message: "将启用当前模块对应的网络服务并重新请求 DHCP。macOS 可能要求管理员授权。",
    confirmLabel: "继续启用",
  });
  if (!confirmed) return;
  const button = $("#enable-network-service");
  const originalLabel = button.textContent;
  button.disabled = true;
  button.textContent = "正在启用…";
  try {
    const result = await api("/api/network/enable-service", { method: "POST" });
    notice(result.summary || "网络服务已处理");
    await Promise.all([loadNetwork(), loadSidebarConnection()]);
  } catch (error) {
    notice(error.message);
  } finally {
    button.disabled = false;
    button.textContent = originalLabel;
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
    if (previous && previous.interface === sample.interface && previous.counter_id === sample.counter_id
        && Number(sample.rx_bytes) >= Number(previous.rx_bytes)
        && Number(sample.tx_bytes) >= Number(previous.tx_bytes)) {
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
    $("#traffic-session-total").title = "当前网卡连接自首次统计起的下载与上传流量之和；服务重启或网卡重新连接后重新统计";
  } catch (error) {
    networkTrafficPrevious = null;
    setValue("#traffic-rx-rate", "--", "muted");
    setValue("#traffic-tx-rate", "--", "muted");
    setValue("#traffic-session-rx", "--", "muted");
    setValue("#traffic-session-tx", "--", "muted");
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

function wait(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

async function waitForUSBNetwork(status, timeoutMilliseconds = 60000) {
  const startedAt = Date.now();
  let lastState = "等待模块重新枚举";
  while (Date.now() - startedAt < timeoutMilliseconds) {
    try {
      const diag = await api("/api/network");
      setUSBNetModeSelector(diag.usbnet_mode);
      if (diag.usb_network_ready) return diag;
      if (diag.network_service?.disabled) {
        lastState = `${diag.network_service.name} 已识别，但 macOS 网络服务处于禁用状态`;
      } else if (diag.usb_network_present) {
        lastState = "USB 网卡已出现，正在等待 DHCP 地址";
      } else if (diag.usb_device) {
        lastState = "模块已重新连接，正在等待 USB 网卡出现";
      } else {
        lastState = "USB 正在重新枚举";
      }
    } catch (error) {
      lastState = `等待设备恢复：${error.message}`;
    }
    const secondsLeft = Math.max(1, Math.ceil((timeoutMilliseconds - (Date.now() - startedAt)) / 1000));
    status.textContent = `${lastState} · 最长等待 ${secondsLeft}s`;
    await wait(2500);
  }
  throw new Error(`${lastState}，未能在 60 秒内完成上网准备`);
}

async function switchWorkMode(mode, label, button) {
  if (button.getAttribute("aria-pressed") === "true") {
    notice(`当前已是${label}`);
    return;
  }
  const confirmed = await showModal({
    title: `切换到${label}`,
    message: mode === 1
      ? "将切换并重启模块，等待 macOS 网卡取得地址，再通过百度、Google 等地址自动验证公网。"
      : `将写入 usbnet=${mode} 并重启模块，USB 会短暂断开。`,
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
    if (mode === 1) {
      status.textContent = "上网模式已写入，等待 USB 网卡和 DHCP...";
      notice("正在准备上网模式");
      const diag = await waitForUSBNetwork(status);
      const interfaceName = diag.network_service?.device || "USB 网卡";
      status.textContent = `${interfaceName} 已取得地址，正在验证公网...`;
      const connectivity = await api("/api/network/check-4g", { method: "POST" });
      renderNetworkCheck("4G 公网", connectivity);
      await Promise.all([loadStatus(), loadNetwork(), loadSidebarConnection()]);
      if (connectivity.ok) {
        status.textContent = `上网模式已就绪 · ${connectivity.summary}`;
        notice("上网模式已就绪");
      } else {
        status.textContent = `上网模式已切换，但${connectivity.summary}`;
        notice(connectivity.summary);
      }
    } else {
      status.textContent = `${label}已写入，等待模块重新枚举后自动刷新。`;
      notice(`${label}切换中`);
      await wait(9000);
      await Promise.all([loadStatus(), loadNetwork(), loadSidebarConnection()]);
      status.textContent = `${label}切换完成。`;
    }
  } catch (error) {
    status.hidden = false;
    status.textContent = `${label}切换失败：${error.message}`;
    notice(error.message);
  } finally {
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
  download.hidden = true;
  profilePanel.hidden = true;
  phonebook.hidden = true;
  list.className = "list empty";
  list.textContent = "正在读取 eUICC";
  status.textContent = "正在通过 AT+CCHO/CGLA 读取 eUICC/eSIM 卡片";
  showESIMCardState("正在识别卡片", "正在确认当前卡片是否支持 eUICC Profile 管理。");
  try {
    const overview = await api("/api/esim");
    if (overview.card_type === "physical_sim") {
      status.textContent = overview.message;
      list.textContent = overview.message;
      showESIMCardState(
        "当前是实体 SIM 卡",
        "短信与蜂窝上网功能可以正常使用；这张卡不包含可管理的 eUICC Profile。",
        "is-physical",
      );
      setESIMHealthPolling(false);
      return;
    }
    hideESIMCardState();
    download.hidden = false;
    profilePanel.hidden = false;
    phonebook.hidden = false;
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
    showESIMCardState("暂时无法读取卡片", error.message, "is-error");
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
let sipActiveCallId = null;
let sipMicMuted = false;
let sipSpeakerMuted = false;
let sipMuteCallId = null;
let sipAudioDevicesLoaded = false;
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

// End reason labels mirror sip.EndReasonLabel; "unknown" is intentionally
// not shown — an undistinguished cause is reported as nothing, not a guess.
function sipEndReasonLabel(reason) {
  return {
    local_hangup: "本机挂断", remote_hangup: "对方挂断", local_reject: "主动拒接",
    remote_cancel: "对方取消", no_answer: "未接超时", no_network: "无网络",
    busy: "忙线", media_failed: "媒体失败", device_disconnected: "设备断开",
    local_cancel: "本机取消",
  }[reason] || "";
}

function formatCallDuration(seconds) {
  const total = Math.max(0, Math.floor(seconds || 0));
  const minutes = Math.floor(total / 60);
  return `${String(minutes).padStart(2, "0")}:${String(total % 60).padStart(2, "0")}`;
}

function sipCallFlags(call) {
  const flags = [];
  if (call.module_connected) flags.push("模块已接通");
  if (call.media_ready) flags.push("媒体就绪");
  return flags.length ? ` · ${flags.join(" · ")}` : "";
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
        const reason = sipEndReasonLabel(call.end_reason);
        const reasonText = reason ? ` · ${reason}` : "";
        const durationText = call.duration_seconds ? ` · ${formatCallDuration(call.duration_seconds)}` : "";
        item.textContent = `${call.direction === "inbound" ? "来电" : "去电"} ${call.number} · ${sipStateLabel(call.state)}${reasonText}${durationText}${sipCallFlags(call)}${call.error ? " · " + call.error : ""}${rtp}`;
        callsList.appendChild(item);
      }
      updateInCallPanel(calls);
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

// In-call panel: DTMF keypad and mic/speaker mute for the live call.
// Mute is per-call on the server, so switching calls resets the toggles.
function updateInCallPanel(calls) {
  const panel = $("#sip-in-call-panel");
  const live = [...calls].reverse().find((c) => ["dialing", "ringing", "active"].includes(c.state)) || null;
  sipActiveCallId = live ? live.id : null;
  panel.hidden = !live;
  if (!live) return;
  if (sipMuteCallId !== live.id) {
    sipMuteCallId = live.id;
    sipMicMuted = false;
    sipSpeakerMuted = false;
  }
  const stateLine = `${sipStateLabel(live.state)}${live.duration_seconds ? ` · ${formatCallDuration(live.duration_seconds)}` : ""}${sipCallFlags(live)}`;
  $("#sip-in-call-state").textContent = stateLine;
  const canControl = live.state === "active" && live.local_audio;
  $("#sip-mic-mute").disabled = !canControl;
  $("#sip-speaker-mute").disabled = !canControl;
  $("#sip-mic-mute").textContent = sipMicMuted ? "取消麦克风静音" : "麦克风静音";
  $("#sip-speaker-mute").textContent = sipSpeakerMuted ? "取消扬声器静音" : "扬声器静音";
  const canDtmf = live.state === "active";
  document.querySelectorAll("#sip-dtmf-pad button").forEach((button) => { button.disabled = !canDtmf; });
}

async function sendSipAudioControls() {
  if (!sipActiveCallId) return;
  try {
    await api("/api/sip/audio-controls", {
      method: "POST",
      body: JSON.stringify({ call_id: sipActiveCallId, microphone_muted: sipMicMuted, speaker_muted: sipSpeakerMuted }),
    });
    $("#sip-in-call-status").textContent = "";
  } catch (error) {
    $("#sip-in-call-status").textContent = `静音设置失败：${error.message}`;
  }
}

$("#sip-mic-mute").addEventListener("click", () => {
  sipMicMuted = !sipMicMuted;
  $("#sip-mic-mute").textContent = sipMicMuted ? "取消麦克风静音" : "麦克风静音";
  void sendSipAudioControls();
});

$("#sip-speaker-mute").addEventListener("click", () => {
  sipSpeakerMuted = !sipSpeakerMuted;
  $("#sip-speaker-mute").textContent = sipSpeakerMuted ? "取消扬声器静音" : "扬声器静音";
  void sendSipAudioControls();
});

$("#sip-dtmf-pad").addEventListener("click", async (event) => {
  const button = event.target.closest("button[data-digit]");
  if (!button || button.disabled || !sipActiveCallId) return;
  const digit = button.dataset.digit;
  try {
    await api("/api/sip/dtmf", { method: "POST", body: JSON.stringify({ call_id: sipActiveCallId, digit }) });
    $("#sip-in-call-status").textContent = `已发送按键 ${digit}`;
  } catch (error) {
    $("#sip-in-call-status").textContent = `按键发送失败：${error.message}`;
  }
});

function fillSipAudioSelect(select, devices, wantInput, selected) {
  select.innerHTML = "";
  const fallback = document.createElement("option");
  fallback.value = "";
  fallback.textContent = "系统默认（内置）";
  select.appendChild(fallback);
  for (const device of devices) {
    if (wantInput && !device.has_input) continue;
    if (!wantInput && !device.has_output) continue;
    const option = document.createElement("option");
    option.value = device.uid;
    option.textContent = device.name;
    if (device.uid === selected || device.name === selected) option.selected = true;
    select.appendChild(option);
  }
  if (selected && ![...select.options].some((o) => o.selected && o.value)) {
    // Stored selection is no longer listed (device unplugged): keep it
    // visible instead of silently falling back to another device.
    const option = document.createElement("option");
    option.value = selected;
    option.textContent = `${selected}（设备不在列表中）`;
    option.selected = true;
    select.appendChild(option);
  }
}

async function loadSipAudioDevices() {
  if (sipAudioDevicesLoaded) return;
  try {
    const data = await api("/api/sip/audio-devices");
    fillSipAudioSelect($("#sip-audio-input"), data.devices || [], true, data.input || "");
    fillSipAudioSelect($("#sip-audio-output"), data.devices || [], false, data.output || "");
    sipAudioDevicesLoaded = true;
    $("#sip-audio-devices-status").textContent = "";
  } catch (error) {
    $("#sip-audio-devices-status").textContent = `音频设备读取失败：${error.message}`;
  }
}

$("#sip-audio-devices-save").addEventListener("click", async () => {
  const status = $("#sip-audio-devices-status");
  status.textContent = "正在保存…";
  try {
    await api("/api/sip/audio-devices", {
      method: "POST",
      body: JSON.stringify({ input: $("#sip-audio-input").value, output: $("#sip-audio-output").value }),
    });
    status.textContent = "音频设备已保存，下次通话生效";
    notice("音频设备已保存，下次通话生效");
  } catch (error) {
    status.textContent = `保存失败：${error.message}`;
  }
});

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

/* delivery-3: 最近通话（持久化历史）——列表复用 GET /api/history/calls；
   回拨走 POST /api/calls/redial（内部转调原有 /api/sip/call），
   发短信走 POST /api/calls/sms（内部转调原有 /api/sms/send），
   保存联系人走 POST /api/calls/save-contact；未接角标来自
   GET /api/calls/missed-count，列表成功渲染后调
   POST /api/calls/missed/seen 清零。 */
let recentCallsState = { scope: "all", direction: "", subscription: "", offset: 0, total: 0, loading: false };
let recentContactsByNormalized = new Map();

async function loadRecentContacts() {
  try {
    const data = await api("/api/contacts");
    recentContactsByNormalized = new Map();
    for (const contact of data.items || []) {
      for (const normalized of contact.normalized || []) recentContactsByNormalized.set(normalized, contact);
    }
  } catch (error) {
    // 联系人名解析失败不阻塞通话列表，只显示原始号码。
  }
}

function recentCallContactName(call) {
  const contact = recentContactsByNormalized.get(call.number_normalized || "");
  return contact ? contact.name : "";
}

async function refreshMissedBadge() {
  const badge = $("#calls-missed-badge");
  if (!badge) return;
  try {
    const data = await api("/api/calls/missed-count");
    const unseen = Number(data.missed_unseen) || 0;
    badge.textContent = unseen > 99 ? "99+" : String(unseen);
    badge.hidden = unseen <= 0;
  } catch (error) {
    badge.hidden = true;
  }
}

function recentCallsQuery() {
  const params = new URLSearchParams({ limit: "50", offset: String(recentCallsState.offset) });
  if (recentCallsState.scope === "missed") params.set("missed", "true");
  if (recentCallsState.direction) params.set("direction", recentCallsState.direction);
  if (recentCallsState.subscription) params.set("subscription_id", recentCallsState.subscription);
  return `/api/history/calls?${params.toString()}`;
}

function recentCallsSubscriptionOptions(items) {
  const select = $("#recent-calls-subscription");
  const previous = select.value;
  const seen = new Map();
  for (const item of items) {
    if (!item.subscription_id || seen.has(item.subscription_id)) continue;
    seen.set(item.subscription_id, item.iccid_masked || item.subscription_id);
  }
  while (select.options.length > 1) select.remove(1);
  for (const [value, label] of seen.entries()) {
    const option = document.createElement("option");
    option.value = value;
    option.textContent = value === "unknown" ? "归属未知" : `卡 ${label}`;
    select.append(option);
  }
  if (previous && seen.has(previous)) select.value = previous;
  else {
    select.value = "";
    recentCallsState.subscription = "";
  }
}

function recentCallRow(call) {
  const row = document.createElement("article");
  row.className = "item";
  const title = document.createElement("strong");
  const name = recentCallContactName(call);
  title.textContent = name ? `${name} · ${call.number}` : call.number;
  const meta = document.createElement("p");
  const direction = call.direction === "inbound" ? "呼入" : "呼出";
  const when = new Date(call.started_at).toLocaleString();
  const duration = call.duration_seconds ? ` · ${formatCallDuration(call.duration_seconds)}` : "";
  const reason = call.end_reason_label ? ` · ${call.end_reason_label}` : "";
  const missed = call.missed ? " · 未接" : "";
  const card = call.attribution_unknown ? " · 归属未知" : (call.iccid_masked ? ` · 卡 ${call.iccid_masked}` : "");
  meta.textContent = `${direction}${missed} · ${when}${duration}${reason}${card}`;
  const actions = document.createElement("div");
  actions.className = "sms-actions";
  const redial = document.createElement("button");
  redial.className = "secondary compact";
  redial.type = "button";
  redial.textContent = "回拨";
  redial.addEventListener("click", () => void redialRecentCall(call));
  const sms = document.createElement("button");
  sms.className = "secondary compact";
  sms.type = "button";
  sms.textContent = "发短信";
  sms.addEventListener("click", () => void smsRecentCall(call));
  const save = document.createElement("button");
  save.className = "secondary compact";
  save.type = "button";
  save.textContent = "保存联系人";
  save.addEventListener("click", () => void saveRecentCallContact(call));
  actions.append(redial, sms, save);
  row.append(title, meta, actions);
  return row;
}

async function redialRecentCall(call) {
  try {
    await api("/api/calls/redial", { method: "POST", body: JSON.stringify({ call_id: call.id }) });
    notice(`已发起回拨：${call.number}`);
    await loadSIP();
    await loadRecentCalls();
  } catch (error) {
    notice(error.message);
  }
}

async function smsRecentCall(call) {
  const values = await showModal({
    title: `发短信给 ${call.number}`,
    message: "经已有短信发送通道发出，发送结果记入发件箱。",
    confirmLabel: "发送",
    fields: [{ name: "message", label: "短信内容", value: "", placeholder: "输入短信内容", required: true }],
  });
  if (!values || !values.message) return;
  try {
    await api("/api/calls/sms", { method: "POST", body: JSON.stringify({ call_id: call.id, message: values.message }) });
    notice("短信已发送");
    await loadSMS();
  } catch (error) {
    notice(error.message);
  }
}

async function saveRecentCallContact(call) {
  const existing = recentContactsByNormalized.get(call.number_normalized || "");
  const values = await showModal({
    title: "保存联系人",
    message: `号码 ${call.number} 将保存在本地联系人中，来电与通话记录会显示该姓名。`,
    confirmLabel: "保存",
    fields: [{ name: "name", label: "姓名", value: existing ? existing.name : "", placeholder: "例如：张三", required: true }],
  });
  if (!values || !values.name) return;
  try {
    await api("/api/calls/save-contact", { method: "POST", body: JSON.stringify({ call_id: call.id, name: values.name }) });
    notice("联系人已保存");
    await loadRecentContacts();
    await loadRecentCalls();
  } catch (error) {
    notice(error.message);
  }
}

async function loadRecentCalls(reset = true) {
  if (recentCallsState.loading) return;
  recentCallsState.loading = true;
  const list = $("#recent-calls-list");
  const status = $("#recent-calls-status");
  const more = $("#recent-calls-more");
  try {
    if (reset) recentCallsState.offset = 0;
    await loadRecentContacts();
    const data = await api(recentCallsQuery());
    const items = data.items || [];
    recentCallsState.total = data.total || 0;
    recentCallsState.offset += items.length;
    recentCallsSubscriptionOptions(items);
    if (reset) list.replaceChildren();
    if (!items.length && reset) {
      list.className = "list empty";
      list.textContent = "暂无通话记录";
    } else {
      list.classList.remove("empty");
      list.append(...items.map((call) => recentCallRow(call)));
    }
    const shown = list.querySelectorAll(".item").length;
    status.textContent = `共 ${recentCallsState.total} 条通话记录 · 已显示 ${shown} 条`;
    more.hidden = shown >= recentCallsState.total || recentCallsState.total === 0;
    // 用户已看到本页未接记录，清零角标；失败不阻塞列表展示。
    try {
      await api("/api/calls/missed/seen", { method: "POST", body: JSON.stringify({}) });
      await refreshMissedBadge();
    } catch (error) { /* 角标下次刷新时再清 */ }
  } catch (error) {
    status.textContent = `读取通话记录失败：${error.message}`;
    notice(error.message);
  } finally {
    recentCallsState.loading = false;
  }
}

$("#recent-calls-scope").addEventListener("change", (event) => {
  recentCallsState.scope = event.currentTarget.value;
  void loadRecentCalls();
});
$("#recent-calls-direction").addEventListener("change", (event) => {
  recentCallsState.direction = event.currentTarget.value;
  void loadRecentCalls();
});
$("#recent-calls-subscription").addEventListener("change", (event) => {
  recentCallsState.subscription = event.currentTarget.value;
  void loadRecentCalls();
});
$("#recent-calls-refresh").addEventListener("click", () => void loadRecentCalls());
$("#recent-calls-more").addEventListener("click", () => void loadRecentCalls(false));

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
    if (tab.dataset.view === "calls") { void loadSIP(); void loadSipAudioDevices(); void loadRecentCalls(); }
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
$("#sim-phone-toggle").addEventListener("click", () => {
  if (!currentSIMPhoneNumber) return;
  simPhoneNumberRevealed = !simPhoneNumberRevealed;
  renderSIMPhoneNumber(currentSIMPhoneNumber, true);
});
$("#sim-phone-copy").addEventListener("click", () => {
  if (currentSIMPhoneNumber) copyIdentifier(currentSIMPhoneNumber, "本机号码");
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
$("#enable-network-service").addEventListener("click", enableNetworkService);
$("#workmode-sms").addEventListener("click", () =>
  switchWorkMode(0, "短信模式", $("#workmode-sms")));
$("#workmode-network").addEventListener("click", () =>
  switchWorkMode(1, "上网模式", $("#workmode-network")));
$("#check-4g-route").addEventListener("click", () =>
  runNetworkCheck("4G 公网", "/api/network/check-4g", $("#check-4g-route")));
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
loadSubscription();
loadSMS();
refreshMissedBadge();
loadSidebarConnection();
setSIPPolling();
setNetworkTrafficPolling(true);
setNetworkActivityPolling(true);
setInterval(loadStatus, 10000);
setInterval(loadSMS, 5000);
setInterval(refreshMissedBadge, 30000);
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

/* delivery-4: 短信会话、搜索、草稿与发送状态 ---------------------------------- */
/* 只读交付点 2 的历史接口 + 本交付点的新会话接口；发送复用 POST /api/sms/send。 */
let convCurrentPeer = ""; // 标准化号码
let convCurrentPeerRaw = "";
let convSearchMode = false;
let convDraftTimer = null;

function convEl(id) { return document.getElementById(id); }

async function loadConversations() {
  const list = convEl("conversations-list");
  const status = convEl("conversations-status");
  try {
    const result = await api("/api/conversations?limit=50");
    const totalUnread = result.items.reduce((n, c) => n + (c.unread_count || 0), 0);
    status.textContent = `共 ${result.total} 个会话 · 未读 ${totalUnread} 条`;
    if (!result.items.length) {
      list.className = "list empty";
      list.textContent = "暂无会话";
      return;
    }
    list.className = "list";
    list.replaceChildren(...result.items.map((conv) => {
      const row = document.createElement("article");
      row.className = "item conv-item";
      row.tabIndex = 0;
      row.setAttribute("role", "button");
      const peer = document.createElement("strong");
      peer.textContent = conv.peer || conv.peer_normalized;
      const preview = document.createElement("p");
      preview.textContent = (conv.last_direction === "out" ? "我：" : "") + (conv.last_body || "");
      const meta = document.createElement("div");
      meta.className = "conv-meta";
      const time = document.createElement("time");
      time.textContent = new Date(conv.last_at).toLocaleString();
      meta.append(time);
      if (conv.unread_count > 0) {
        const badge = document.createElement("span");
        badge.className = "conv-unread-badge";
        badge.textContent = `未读 ${conv.unread_count}`;
        meta.append(badge);
      }
      if (conv.last_direction === "out" && conv.last_outbox_status_label) {
        const st = document.createElement("small");
        st.textContent = conv.last_outbox_status_label;
        meta.append(st);
      }
      const sub = document.createElement("small");
      sub.textContent = conv.attribution_unknown ? "归属未知" : `卡 ${conv.subscription_id}`;
      meta.append(sub);
      row.append(peer, preview, meta);
      const open = () => void openConversation(conv.peer_normalized, conv.peer || conv.peer_normalized);
      row.addEventListener("click", open);
      row.addEventListener("keydown", (event) => { if (event.key === "Enter") open(); });
      return row;
    }));
  } catch (error) {
    status.textContent = `会话加载失败：${error.message}`;
  }
}

async function searchConversations() {
  const q = convEl("conv-search").value.trim();
  const list = convEl("conversations-list");
  if (!q) {
    convSearchMode = false;
    await loadConversations();
    return;
  }
  convSearchMode = true;
  try {
    const result = await api(`/api/conversations/search?q=${encodeURIComponent(q)}&limit=50`);
    convEl("conversations-status").textContent = `搜索“${q}”：${result.total} 条`;
    if (!result.items.length) {
      list.className = "list empty";
      list.textContent = "没有匹配的短信";
      return;
    }
    list.className = "list";
    list.replaceChildren(...result.items.map((msg) => {
      const row = document.createElement("article");
      row.className = "item conv-item";
      const peer = document.createElement("strong");
      peer.textContent = msg.peer || msg.peer_normalized;
      const content = document.createElement("p");
      content.textContent = msg.body;
      const time = document.createElement("time");
      time.textContent = new Date(msg.received_at).toLocaleString();
      row.append(peer, content, time);
      row.addEventListener("click", () => void openConversation(msg.peer_normalized, msg.peer || msg.peer_normalized));
      return row;
    }));
  } catch (error) {
    notice(error.message);
  }
}

async function openConversation(peerNormalized, peerRaw) {
  convCurrentPeer = peerNormalized;
  convCurrentPeerRaw = peerRaw;
  convEl("conversation-thread").hidden = false;
  convEl("thread-peer").textContent = peerRaw;
  convEl("thread-message").value = "";
  convEl("thread-draft-status").textContent = "";
  convEl("thread-send-status").textContent = "";
  await loadThread();
  try {
    await api("/api/conversations/read", { method: "POST", body: JSON.stringify({ peer: peerNormalized }) });
  } catch (error) { /* 未读标记失败不阻塞看消息 */ }
  await loadConversations();
  try {
    const drafts = await api("/api/conversations/drafts");
    const draft = (drafts.items || []).find((d) => d.peer_normalized === peerNormalized);
    if (draft && draft.body) {
      convEl("thread-message").value = draft.body;
      convEl("thread-draft-status").textContent = `已恢复草稿 · ${new Date(draft.updated_at).toLocaleString()}`;
    }
  } catch (error) { /* 草稿读失败不阻塞 */ }
  convEl("thread-message").focus();
}

async function loadThread() {
  const box = convEl("thread-messages");
  try {
    const result = await api(`/api/conversations/thread?peer=${encodeURIComponent(convCurrentPeer)}&limit=50`);
    convEl("thread-meta").textContent = `共 ${result.total} 条 · ${convCurrentPeerRaw}`;
    if (!result.items.length) {
      box.className = "list empty";
      box.textContent = "暂无消息";
      return;
    }
    box.className = "list conv-thread-list";
    box.replaceChildren(...result.items.map((msg) => {
      const row = document.createElement("article");
      row.className = `item conv-message ${msg.direction === "out" ? "conv-out" : "conv-in"}`;
      const who = document.createElement("strong");
      who.textContent = msg.direction === "out" ? "我" : (msg.peer || msg.peer_normalized);
      const content = document.createElement("p");
      content.textContent = msg.body;
      const meta = document.createElement("div");
      meta.className = "conv-meta";
      const time = document.createElement("time");
      time.textContent = new Date(msg.received_at).toLocaleString();
      meta.append(time);
      if (msg.direction === "out" && msg.outbox_status_label) {
        const st = document.createElement("small");
        st.textContent = msg.outbox_status_label;
        if (msg.outbox_status === "uncertain") st.textContent += "（无法确认是否已发送，请勿重复发送）";
        if (msg.outbox_status === "failed") st.textContent += "（已证明未发送，可重发）";
        meta.append(st);
      }
      if (msg.concat_total) {
        const seg = document.createElement("small");
        seg.textContent = `长短信 ${msg.concat_segments || 1}/${msg.concat_total} 段`;
        meta.append(seg);
      }
      row.append(who, content, meta);
      return row;
    }));
  } catch (error) {
    box.className = "list empty";
    box.textContent = `加载失败：${error.message}`;
  }
}

function scheduleDraftSave() {
  clearTimeout(convDraftTimer);
  convDraftTimer = setTimeout(async () => {
    if (!convCurrentPeer) return;
    const body = convEl("thread-message").value;
    try {
      await api("/api/conversations/drafts", { method: "PUT", body: JSON.stringify({ peer: convCurrentPeer, body }) });
      convEl("thread-draft-status").textContent = body.trim() ? `草稿已保存 · ${new Date().toLocaleTimeString()}` : "草稿已清空";
    } catch (error) {
      convEl("thread-draft-status").textContent = `草稿保存失败：${error.message}`;
    }
  }, 800);
}

function downloadExport(peer) {
  const params = new URLSearchParams({ format: "csv" });
  if (peer) params.set("peer", peer);
  const a = document.createElement("a");
  a.href = `/api/conversations/export?${params.toString()}`;
  a.download = "";
  document.body.append(a);
  a.click();
  a.remove();
}

convEl("conv-search-btn").addEventListener("click", () => void searchConversations());
convEl("conv-search").addEventListener("keydown", (event) => {
  if (event.key === "Enter") { event.preventDefault(); void searchConversations(); }
});
convEl("conv-search-clear").addEventListener("click", async () => {
  convEl("conv-search").value = "";
  convSearchMode = false;
  await loadConversations();
});
convEl("conv-export-all").addEventListener("click", () => downloadExport(""));
convEl("thread-export").addEventListener("click", () => downloadExport(convCurrentPeer));
convEl("thread-close").addEventListener("click", async () => {
  convCurrentPeer = "";
  convCurrentPeerRaw = "";
  convEl("conversation-thread").hidden = true;
  await loadConversations();
});
convEl("thread-message").addEventListener("input", scheduleDraftSave);
convEl("thread-draft-discard").addEventListener("click", async () => {
  if (!convCurrentPeer) return;
  try {
    await api(`/api/conversations/drafts?peer=${encodeURIComponent(convCurrentPeer)}`, { method: "DELETE" });
    convEl("thread-message").value = "";
    convEl("thread-draft-status").textContent = "草稿已丢弃";
  } catch (error) {
    notice(error.message);
  }
});
convEl("thread-send-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const input = convEl("thread-message");
  const text = input.value.trim();
  if (!text || !convCurrentPeer) return;
  const status = convEl("thread-send-status");
  status.textContent = "正在发送…";
  try {
    const result = await api("/api/sms/send", { method: "POST", body: JSON.stringify({ phone: convCurrentPeer, message: text }) });
    input.value = "";
    try {
      await api(`/api/conversations/drafts?peer=${encodeURIComponent(convCurrentPeer)}`, { method: "DELETE" });
    } catch (error) { /* 发送成功后草稿删失败不阻塞 */ }
    convEl("thread-draft-status").textContent = "";
    status.textContent = `已提交（${result.segments || 1} 段）· 送达状态见上方消息`;
    await loadThread();
    await loadConversations();
  } catch (error) {
    status.textContent = `发送失败：${error.message}（已记为“结果不确定”，请勿重复发送）`;
  }
});
document.querySelector('.tab[data-view="sms"]').addEventListener("click", () => void loadConversations());
loadConversations();
setInterval(() => {
  if (!convSearchMode && $("#sms").classList.contains("active") && !convCurrentPeer) void loadConversations();
}, 30000);
