document.addEventListener("htmx:sendError", () => {
  document.getElementById("connection-error")?.classList.remove("d-none");
});
document.addEventListener("htmx:timeout", () => {
  document.getElementById("connection-error")?.classList.remove("d-none");
});
document.addEventListener("htmx:afterRequest", (event) => {
  if (event.detail.successful) {
    document.getElementById("connection-error")?.classList.add("d-none");
  }
});
document.addEventListener("htmx:configRequest", (event) => {
  const csrf = document.querySelector('meta[name="csrf-token"]')?.content;
  if (csrf) event.detail.headers["X-CSRF-Token"] = csrf;
});
function configureCredentialForms(root = document) {
  root.querySelectorAll(".credential-form").forEach((form) => {
    const update = () => {
      const kind = form.elements.auth_type.value;
      const replace = form.dataset.new === "true" || form.elements.replace_credentials?.checked;
      form.querySelectorAll("[data-auth-section]").forEach((section) => {
        const enabled = section.dataset.authSection.split(" ").includes(kind);
        section.hidden = !enabled;
        section.querySelectorAll("input, select, textarea").forEach((field) => {
          field.disabled = !enabled;
          if (field.hasAttribute("data-credential-required")) field.required = enabled && replace;
        });
      });
      const proxy = form.querySelector("[data-proxy-section]");
      proxy.hidden = form.elements.route_mode.value !== "socks5";
      form.elements.proxy_id.required = !proxy.hidden;
    };
    if (!form.dataset.initialized) { form.addEventListener("change", update); form.dataset.initialized = "true"; }
    update();
  });
}
document.addEventListener("DOMContentLoaded", () => configureCredentialForms());
document.addEventListener("htmx:afterSwap", () => configureCredentialForms());

function watchJob() {
  const root = document.querySelector("[data-job-watch]");
  if (!root) return;
  const status = root.querySelector("[data-job-live-status]");
  let source = null, stopped = false, pending = false, again = false, refreshTimer = null;
  let streamHealthy = false;
  const settled = () => root.querySelector("#job-content")?.dataset.terminal === "true";
  const close = () => { source?.close(); source = null; streamHealthy = false; };
  const refresh = async () => {
    if (stopped) return;
    if (pending) { again = true; return; }
    pending = true;
    try {
      const response = await fetch(root.dataset.fragmentUrl, {credentials: "same-origin", headers: {"HX-Request": "true"}});
      if (response.status === 401) { stopped = true; close(); window.location.assign("/login"); return; }
      if (response.status === 403 || response.status === 404) { stopped = true; close(); root.querySelector("#job-content").replaceChildren(); status.textContent = "任务不可访问，权限可能已变更。"; return; }
      if (!response.ok) throw new Error("refresh unavailable");
      const template = document.createElement("template");
      template.innerHTML = await response.text();
      const content = template.content.firstElementChild;
      if (content?.id !== "job-content") throw new Error("unexpected fragment");
      root.querySelector("#job-content").replaceWith(content);
      window.htmx?.process(content);
      if (settled()) { close(); status.textContent = "已显示最终结果"; }
      else if (!source && !stopped) connect();
    } catch { status.textContent = "暂时无法刷新，将自动重试。"; }
    finally { pending = false; if (again) { again = false; scheduleRefresh(); } }
  };
  const scheduleRefresh = () => {
    if (refreshTimer) return;
    refreshTimer = setTimeout(() => { refreshTimer = null; refresh(); }, 150);
  };
  const connect = () => {
    if (stopped || settled() || source || !window.EventSource) return;
    const after = root.querySelector("#job-content")?.dataset.eventSeq || "0";
    source = new EventSource(`${root.dataset.eventsUrl}?after=${encodeURIComponent(after)}`);
    source.onopen = () => { streamHealthy = true; status.textContent = "进度已连接"; };
    source.addEventListener("progress", scheduleRefresh);
    source.addEventListener("complete", () => { close(); scheduleRefresh(); });
    source.addEventListener("access_revoked", () => { close(); scheduleRefresh(); });
    source.onerror = () => { streamHealthy = false; status.textContent = "实时连接中断，正在定时刷新。"; };
  };
  root.querySelector("[data-job-refresh]").addEventListener("click", scheduleRefresh);
  const polling = setInterval(() => { if (!stopped && !settled() && !streamHealthy) scheduleRefresh(); }, 3000);
  window.addEventListener("pagehide", () => { stopped = true; close(); clearInterval(polling); clearTimeout(refreshTimer); }, {once: true});
  if (settled()) status.textContent = "已显示最终结果";
  else { status.textContent = "正在连接进度更新"; connect(); }
}
document.addEventListener("DOMContentLoaded", watchJob);

function configureInventoryTargets(root = document) {
  root.querySelectorAll("[data-inventory-targets]").forEach((container) => {
    const rows = [...container.querySelectorAll("[data-inventory-target-row]")];
    rows.forEach((row) => {
      const kind = row.querySelector('[name="kind"]').value;
      const region = row.querySelector('[name="region"]');
      if (row.dataset.previousKind === "lightsail_instance" && kind !== "lightsail_instance") row.dataset.instanceRegion = region.value;
      if (kind !== "lightsail_instance") region.value = "us-east-1";
      else if (row.dataset.previousKind && row.dataset.previousKind !== kind) region.value = row.dataset.instanceRegion || "us-east-1";
      region.readOnly = kind !== "lightsail_instance";
      row.dataset.previousKind = kind;
      row.querySelector("[data-inventory-remove]").disabled = rows.length <= 1;
    });
    const add = container.closest("form")?.querySelector("[data-inventory-add]");
    if (add) add.disabled = rows.length >= 50;
  });
}
document.addEventListener("click", (event) => {
  const add = event.target.closest("[data-inventory-add]");
  const remove = event.target.closest("[data-inventory-remove]");
  if (add) {
    const container = add.closest("form").querySelector("[data-inventory-targets]");
    const rows = container.querySelectorAll("[data-inventory-target-row]");
    if (!rows.length || rows.length >= 50) return;
    const row = rows[rows.length - 1].cloneNode(true);
    container.append(row);
    configureInventoryTargets();
    row.querySelector("select").focus();
  } else if (remove) {
    const container = remove.closest("[data-inventory-targets]");
    if (container.querySelectorAll("[data-inventory-target-row]").length <= 1) return;
    remove.closest("[data-inventory-target-row]").remove();
    configureInventoryTargets();
  }
});
document.addEventListener("change", (event) => { if (event.target.closest("[data-inventory-target-row]")) configureInventoryTargets(); });
document.addEventListener("DOMContentLoaded", () => configureInventoryTargets());
document.addEventListener("htmx:afterSwap", () => configureInventoryTargets());

const launchCatalogues = new WeakMap();
const launchRequests = new WeakMap();
function launchField(row, name) { return row.querySelector(`[name="${name}"]`); }
function launchChoices(field, values, label, id = value => value) {
  const previous = field.value;
  field.replaceChildren(...values.map(value => new Option(label(value), id(value))));
  if (values.some(value => id(value) === previous)) field.value = previous;
}
function launchBundles(row) {
  const data = launchCatalogues.get(row);
  if (!data) return;
  const blueprint = data.blueprints.find(value => value.id === launchField(row, "blueprint_id").value);
  const ipv4 = launchField(row, "ip_type").value !== "ipv6";
  launchChoices(launchField(row, "bundle_id"), data.bundles.filter(value => value.public_ipv4 === ipv4 && value.power >= (blueprint?.min_power ?? Infinity)), value => `${value.id} · ${value.cpu} vCPU / ${value.ram_gb} GB / ${value.disk_gb} GB · $${(value.monthly_cents / 100).toFixed(2)}/月`, value => value.id);
}
function launchButtons(form) {
  const rows = form.querySelectorAll("[data-launch-group]");
  rows.forEach(row => { row.querySelector("[data-remove-launch]").disabled = rows.length === 1; });
  form.querySelector("[data-add-launch]").disabled = rows.length >= 50;
}
async function loadLaunchCatalogue(row) {
  launchRequests.get(row)?.abort();
  const request = new AbortController();
  launchRequests.set(row, request);
  const status = row.querySelector("[data-catalogue-status]");
  const connection = launchField(row, "connection_id").value;
  const region = launchField(row, "region").value;
  if (!connection || !region) { status.textContent = "请先选择连接并填写区域。"; return; }
  launchCatalogues.delete(row);
  status.textContent = "正在查询区域、镜像、套餐和配额…";
  row.querySelector("[data-catalogue-quota]").textContent = "正在查询配额…";
  const button = row.querySelector("[data-load-catalogue]"); button.disabled = true;
  try {
    const response = await fetch(`/api/v1/lightsail/catalogue?${new URLSearchParams({connection_id: connection, region})}`, {credentials: "same-origin", signal: request.signal});
    if (response.status === 401) { window.location.assign("/login"); return; }
    const envelope = await response.json();
    if (!response.ok) throw new Error(envelope.error?.message || "配置暂时无法加载，请检查连接与权限。");
    const data = envelope.data;
    if (!row.isConnected || connection !== launchField(row, "connection_id").value || region !== launchField(row, "region").value) return;
    launchCatalogues.set(row, data);
    launchChoices(launchField(row, "zone"), data.zones, value => value);
    launchChoices(launchField(row, "blueprint_id"), data.blueprints, value => `${value.name} ${value.version}`, value => value.id);
    launchChoices(launchField(row, "key_pair_name"), ["", ...data.key_pairs], value => value || "区域默认密钥对");
    launchBundles(row);
    configureLaunchLogin(row);
    status.textContent = "配置已加载；预览和执行前会再次核对。";
    row.querySelector("[data-catalogue-quota]").textContent = data.quota.known ? `区域 vCPU：已用 ${data.quota.used_vcpu} / 配额 ${data.quota.limit_vcpu}` : "配额无法查询，执行时由 AWS 判定。";
  } catch (error) {
    if (error.name !== "AbortError") { status.textContent = error.message; row.querySelector("[data-catalogue-quota]").textContent = "配额未更新，请重新加载配置。"; }
  } finally {
    if (launchRequests.get(row) === request) button.disabled = false;
  }
}
document.addEventListener("click", event => {
  const load = event.target.closest("[data-load-catalogue]");
  const add = event.target.closest("[data-add-launch]");
  const remove = event.target.closest("[data-remove-launch]");
  if (load) loadLaunchCatalogue(load.closest("[data-launch-group]"));
  else if (add) {
    const form = add.closest("form"), rows = form.querySelectorAll("[data-launch-group]");
    if (!rows.length || rows.length >= 50) return;
    const previous = rows[rows.length - 1], row = previous.cloneNode(true);
    for (const name of ["connection_id", "region", "zone", "blueprint_id", "bundle_id", "ip_type", "key_pair_name", "login_method"]) launchField(row,name).value = launchField(previous,name).value;
    launchField(row, "names").value = "";
    launchField(row,"root_password").value="";launchField(row,"root_password_confirm").value="";
    row.querySelector("[data-load-catalogue]").disabled = false;
    row.querySelector("[data-catalogue-status]").textContent = "请加载此配置的最新镜像与套餐。";
    row.querySelector("[data-catalogue-quota]").textContent = "配额尚未查询";
    delete row.dataset.regionsInitialized; form.querySelector("[data-launch-groups]").append(row); launchButtons(form); configureLaunchForms(); launchField(row,"connection_id").focus();
  } else if (remove) {
    const form = remove.closest("form"), row = remove.closest("[data-launch-group]");
    if (form.querySelectorAll("[data-launch-group]").length <= 1) return;
    launchRequests.get(row)?.abort(); launchRegionRequests.get(row)?.abort(); row.remove(); launchButtons(form);
  }
});
document.addEventListener("change", event => {
  const row = event.target.closest("[data-launch-group]");
  if (!row) return;
  if (["connection_id", "region"].includes(event.target.name)) {
    launchRequests.get(row)?.abort(); launchCatalogues.delete(row);
    row.querySelector("[data-catalogue-status]").textContent = "连接或区域已变化，请重新加载可用配置。";
    row.querySelector("[data-catalogue-quota]").textContent = "配额尚未查询";
    for (const field of row.querySelectorAll("[data-catalogue-field]")) field.replaceChildren(new Option("等待加载", ""));
    if (event.target.name === "connection_id") loadLaunchRegions(row);
  } else if (["blueprint_id", "ip_type"].includes(event.target.name)) {launchBundles(row);configureLaunchLogin(row);}
  else if (event.target.name === "login_method") configureLaunchLogin(row);
});
document.addEventListener("submit", event => {
  const form = event.target.closest("[data-lightsail-form]");
  if (!form) return;
  if ([...form.querySelectorAll("[data-launch-group]")].some(row => !launchCatalogues.has(row))) {
    event.preventDefault(); event.stopImmediatePropagation();
    const message = form.querySelector("[data-launch-error]"); message.textContent = "请为每组配置加载最新的可用镜像和套餐。"; message.classList.remove("d-none");
  }
}, true);
function configureLaunchForms() { document.querySelectorAll("[data-lightsail-form]").forEach(form=>{launchButtons(form);form.querySelectorAll("[data-launch-group]").forEach(row=>{configureLaunchLogin(row);if(!row.dataset.regionsInitialized){row.dataset.regionsInitialized="true";if(launchField(row,"connection_id").value)loadLaunchRegions(row);}});}); }
document.addEventListener("DOMContentLoaded", configureLaunchForms);
document.addEventListener("htmx:afterSwap", configureLaunchForms);

function awsRegionState(value) {
  return ({ENABLED:"已启用", ENABLED_BY_DEFAULT:"默认启用", ENABLING:"启用中", DISABLED:"未启用", DISABLING:"停用中", UNKNOWN:"状态未知"})[value] || value;
}
const regionSelectionRequests = new WeakMap();
async function loadSelectableRegions(block) {
  const input = block.querySelector('[name="connection_ids"]');
  const panel = block.querySelector("[data-region-directory]");
  panel.hidden = !input.checked;
  regionSelectionRequests.get(block)?.abort();
  if (!input.checked) { panel.querySelector("[data-region-choices]").replaceChildren(); return; }
  const request = new AbortController(); regionSelectionRequests.set(block, request);
  const form = block.closest("form");
  const selected = new Set(JSON.parse(form.dataset.selected || "[]"));
  panel.querySelectorAll('[name="targets"]:checked').forEach(field => selected.add(field.value));
  const status = panel.querySelector("[data-region-status]"), choices = panel.querySelector("[data-region-choices]");
  const all = panel.querySelector("[data-region-select-all]"); all.disabled = true;
  choices.replaceChildren(); status.textContent = "正在读取该账号的区域状态…";
  try {
    const response = await fetch(`/api/v1/regions/catalogue?${new URLSearchParams({connection_id: input.value})}`,{credentials:"same-origin", signal:request.signal});
    if (response.status === 401) { window.location.assign("/login"); return; }
    const envelope = await response.json();
    if (!response.ok) throw new Error(envelope.error?.message || "区域暂时无法加载，请检查连接。");
    if (!block.isConnected || !input.checked || regionSelectionRequests.get(block) !== request) return;
    for (const region of envelope.data.regions) {
      const column = document.createElement("div"); column.className = "col-md-6 col-xl-4";
      const label = document.createElement("label"); label.className = "form-check border rounded p-3 ps-5 mb-0 h-100";
      const checkbox = document.createElement("input"); checkbox.className = "form-check-input"; checkbox.type = "checkbox"; checkbox.name = "targets"; checkbox.value = `${input.value}/${region.code}`; checkbox.disabled = !region.selectable; checkbox.checked = region.selectable && selected.has(checkbox.value);
      const caption = document.createElement("span"); caption.className = "form-check-label"; caption.textContent = awsRegionNames[region.code] ? `${awsRegionNames[region.code]} · ${region.code}` : region.code;
      const state = document.createElement("span"); state.className = "d-block small text-secondary mt-1"; state.textContent = awsRegionState(region.state);
      label.append(checkbox,caption,state);column.append(label);choices.append(column);
    }
    all.disabled = !envelope.data.regions.some(region => region.selectable);
    status.textContent = `已加载 ${envelope.data.regions.length} 个区域，请勾选需要启用的区域。`;
  } catch (error) { if (error.name !== "AbortError") status.textContent = error.message; }
}
function configureRegionSelection() {
  document.querySelectorAll("[data-region-selector]").forEach(form => {
    if (form.dataset.initialized) return; form.dataset.initialized = "true";
    form.querySelectorAll('[name="connection_ids"]:checked').forEach(field => loadSelectableRegions(field.closest("[data-region-connection]")));
  });
}
document.addEventListener("change",event => {
  if (!event.target.matches('[data-region-selector] [name="connection_ids"]')) return;
  const field = event.target, block = field.closest("[data-region-connection]");
  if (field.checked) field.closest("[data-region-account]").querySelectorAll('[name="connection_ids"]').forEach(other => {
    if (other !== field && other.checked) { other.checked = false; loadSelectableRegions(other.closest("[data-region-connection]")); }
  });
  loadSelectableRegions(block);
});
document.addEventListener("click",event => {
  const reload = event.target.closest("[data-region-reload]"), all = event.target.closest("[data-region-select-all]");
  if (reload) loadSelectableRegions(reload.closest("[data-region-connection]"));
  if (all) {
    const fields = [...all.closest("[data-region-directory]").querySelectorAll('[name="targets"]:not(:disabled)')];
    const checked = !fields.every(field => field.checked); fields.forEach(field => {field.checked = checked;});
  }
});
document.addEventListener("DOMContentLoaded",configureRegionSelection);
document.addEventListener("htmx:afterSwap",configureRegionSelection);

const launchRegionRequests = new WeakMap();
async function loadLaunchRegions(row) {
  launchRegionRequests.get(row)?.abort();
  const request = new AbortController(); launchRegionRequests.set(row,request);
  const connection = launchField(row,"connection_id").value;
  const region = launchField(row,"region"), previous = region.value;
  const status = row.querySelector("[data-launch-region-status]");
  region.replaceChildren(new Option(connection ? "正在加载区域…" : "先选择访问连接", ""));
  if (!connection) {status.textContent = "选择连接后列出 Lightsail 支持的区域。";return;}
  status.textContent = "正在查询可用区域…";
  try {
    const response = await fetch(`/api/v1/lightsail/regions?${new URLSearchParams({connection_id:connection})}`,{credentials:"same-origin",signal:request.signal});
    if (response.status === 401) {window.location.assign("/login");return;}
    const envelope = await response.json();
    if (!response.ok) throw new Error(envelope.error?.message || "区域列表暂不可用，请检查连接。");
    if (!row.isConnected || launchField(row,"connection_id").value !== connection || launchRegionRequests.get(row) !== request) return;
    region.replaceChildren(new Option("请选择区域", ""));
    for (const value of envelope.data.items) {
      const option = new Option(`${awsRegionNames[value.code] || value.name} · ${value.code}${value.available ? "" : " · " + awsRegionState(value.state)}`,value.code);
      option.disabled = !value.available; region.append(option);
    }
    if (envelope.data.items.some(value=>value.code===previous && value.available)) region.value=previous;
    status.textContent = "仅可选择账号已启用且提供可用区的区域。";
  } catch(error) {
    if(error.name!=="AbortError") {region.replaceChildren(new Option("区域加载失败", ""));status.textContent=error.message;}
  }
}
document.addEventListener("click",event=>{const button=event.target.closest("[data-reload-regions]");if(button)loadLaunchRegions(button.closest("[data-launch-group]"));});

function configureLaunchLogin(row) {
 const method=launchField(row,"login_method"),password=launchField(row,"root_password"),confirm=launchField(row,"root_password_confirm");
 const catalogue=launchCatalogues.get(row);
 const blueprint=catalogue?.blueprints.find(value=>value.id===launchField(row,"blueprint_id").value);
 const option=[...method.options].find(value=>value.value==="root_password");
 option.disabled=!!catalogue&&!blueprint?.root_password_supported;
 if(option.disabled&&method.value==="root_password") {method.value="ssh_key";row.querySelector("[data-launch-login-hint]").textContent="此镜像不支持自动设置 root 密码，请选择 SSH 密钥或更换镜像。";}
 else row.querySelector("[data-launch-login-hint]").textContent="root 密码支持 Debian、Ubuntu 和 Amazon Linux 系统镜像。密码方式无需选择密钥对。";
 const enabled=method.value==="root_password";
 row.querySelector("[data-launch-password-fields]").hidden=!enabled;
 row.querySelector("[data-launch-key-pair]").hidden=enabled;
 password.required=confirm.required=enabled;
 if(!enabled){password.value="";confirm.value="";confirm.setCustomValidity("");}
 else launchField(row,"key_pair_name").value="";
}
document.addEventListener("input",event=>{
 const row=event.target.closest("[data-launch-group]");
 if(!row||!["root_password","root_password_confirm"].includes(event.target.name))return;
 const password=launchField(row,"root_password"),confirm=launchField(row,"root_password_confirm");
 confirm.setCustomValidity(confirm.value&&confirm.value!==password.value?"两次输入的密码不一致":"");
});

// Labels only; availability and opt-in state always come from AWS responses.
// https://docs.aws.amazon.com/global-infrastructure/latest/regions/aws-regions.html
const awsRegionNames = Object.freeze({
 "us-east-1":"美国东部（弗吉尼亚北部）","us-east-2":"美国东部（俄亥俄）","us-west-1":"美国西部（加利福尼亚北部）","us-west-2":"美国西部（俄勒冈）",
 "af-south-1":"非洲（开普敦）","ap-east-1":"亚太地区（香港）","ap-east-2":"亚太地区（台北）",
 "ap-south-1":"亚太地区（孟买）","ap-south-2":"亚太地区（海得拉巴）",
 "ap-northeast-1":"亚太地区（东京）","ap-northeast-2":"亚太地区（首尔）","ap-northeast-3":"亚太地区（大阪）",
 "ap-southeast-1":"亚太地区（新加坡）","ap-southeast-2":"亚太地区（悉尼）","ap-southeast-3":"亚太地区（雅加达）","ap-southeast-4":"亚太地区（墨尔本）","ap-southeast-5":"亚太地区（马来西亚）","ap-southeast-6":"亚太地区（新西兰）","ap-southeast-7":"亚太地区（泰国）",
 "ca-central-1":"加拿大（中部）","ca-west-1":"加拿大西部（卡尔加里）",
 "eu-central-1":"欧洲（法兰克福）","eu-central-2":"欧洲（苏黎世）","eu-west-1":"欧洲（爱尔兰）","eu-west-2":"欧洲（伦敦）","eu-west-3":"欧洲（巴黎）","eu-south-1":"欧洲（米兰）","eu-south-2":"欧洲（西班牙）","eu-north-1":"欧洲（斯德哥尔摩）",
 "il-central-1":"以色列（特拉维夫）","mx-central-1":"墨西哥（中部）","me-south-1":"中东（巴林）","me-central-1":"中东（阿联酋）","sa-east-1":"南美洲（圣保罗）"
});
