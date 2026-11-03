function connectionNotice(event){const notice=document.getElementById('connection-error');if(notice?.hasAttribute('data-dashboard-poll-error')&&event.detail?.target?.id!=='dashboard-content')return null;return notice;}
document.addEventListener('htmx:sendError',event=>connectionNotice(event)?.classList.remove('d-none'));
document.addEventListener('htmx:timeout',event=>connectionNotice(event)?.classList.remove('d-none'));
document.addEventListener('htmx:afterRequest',event=>{if(event.detail.successful)connectionNotice(event)?.classList.add('d-none');});
document.addEventListener('dashboard:error',()=>document.querySelector('[data-dashboard-poll-error]')?.classList.remove('d-none'));
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
      proxy.hidden = !["socks5","http","proxy"].includes(form.elements.route_mode.value);
      form.elements.proxy_id.required = !proxy.hidden;form.elements.proxy_id.disabled=proxy.hidden;
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
      const regional = !["account_region","lightsail_region"].includes(kind);
      if (row.dataset.previousKind && !["account_region","lightsail_region"].includes(row.dataset.previousKind) && !regional) row.dataset.instanceRegion = region.value;
      if (!regional) region.value = "us-east-1";
      else if (row.dataset.previousKind && row.dataset.previousKind !== kind) region.value = row.dataset.instanceRegion || "us-east-1";
      region.readOnly = !regional;
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

function observationStatus(data, keys) {
  const values = Object.entries(data.freshness || {}).filter(([key]) => !keys || keys.includes(key)).map(([, value]) => value);
  if (!values.length) return "已读取最新数据";
  const updated = new Date(Math.min(...values.map(value => new Date(value.updated_at).getTime())));
  const time = updated.toLocaleString("zh-CN", {timeZone:"Asia/Shanghai", month:"2-digit", day:"2-digit", hour:"2-digit", minute:"2-digit", hour12:false});
  const state = values.some(value => value.refresh_failed) ? "更新暂未成功，保留上次数据" : values.some(value => value.refreshing) ? "已显示上次数据，后台更新中" : values.some(value => value.cached) ? "已加载缓存" : "已从 AWS 更新";
  return `${state} · 更新于 ${time}（北京时间）`;
}

function directoryStatus(data,keys){return `${observationStatus(data,keys)}；预览和执行前会再次核对。`;}

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
async function loadLaunchCatalogue(row, refresh = false) {
  launchRequests.get(row)?.abort();
  const request = new AbortController();
  launchRequests.set(row, request);
  const status = row.querySelector("[data-catalogue-status]");
  const connection = launchField(row, "connection_id").value;
  const region = launchField(row, "region").value;
  if (!connection || !region) { status.textContent = "请先选择连接并填写区域。"; return; }
  status.textContent = refresh ? "正在从 AWS 更新配置…" : "正在加载可用配置…";
  row.querySelector("[data-catalogue-quota]").textContent = "正在查询配额…";
  const button = row.querySelector("[data-load-catalogue]"); button.disabled = true;
  try {
    const response = await fetch(`/api/v1/lightsail/catalogue?${new URLSearchParams({connection_id: connection, region, refresh:String(refresh)})}`, {credentials: "same-origin", signal: request.signal});
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
    status.textContent = directoryStatus(data);
    row.querySelector("[data-catalogue-quota]").textContent = data.quota.known ? `区域 vCPU：已用 ${data.quota.used_vcpu} / 配额 ${data.quota.limit_vcpu}${data.freshness?.environment?.cached ? "（最近快照）" : ""}` : "配额无法查询，执行时由 AWS 判定。";
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
  if (load) loadLaunchCatalogue(load.closest("[data-launch-group]"), true);
  else if (add) {
    const form = add.closest("form"), rows = form.querySelectorAll("[data-launch-group]");
    if (!rows.length || rows.length >= 50) return;
    const previous = rows[rows.length - 1], row = previous.cloneNode(true);
    for (const name of ["connection_id", "region", "zone", "blueprint_id", "bundle_id", "ip_type", "key_pair_name", "login_method"]) launchField(row,name).value = launchField(previous,name).value;
    launchField(row, "names").value = "";
    launchField(row,"root_password").value="";launchField(row,"root_password_confirm").value="";
    row.querySelector("[data-load-catalogue]").disabled = false;
    row.querySelector("[data-catalogue-status]").textContent = "正在加载此配置的镜像与套餐。";
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
    row.querySelector("[data-catalogue-status]").textContent = "选择区域后自动加载可用配置。";
    row.querySelector("[data-catalogue-quota]").textContent = "配额尚未查询";
    for (const field of row.querySelectorAll("[data-catalogue-field]")) field.replaceChildren(new Option("等待加载", ""));
    if (event.target.name === "connection_id") loadLaunchRegions(row);
    else if (launchField(row,"region").value) loadLaunchCatalogue(row);
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
async function loadSelectableRegions(block, refresh = false) {
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
    const response = await fetch(`/api/v1/regions/catalogue?${new URLSearchParams({connection_id: input.value,refresh:String(refresh)})}`,{credentials:"same-origin", signal:request.signal});
    if (response.status === 401) { window.location.assign("/login"); return; }
    const envelope = await response.json();
    if (!response.ok) throw new Error(envelope.error?.message || "区域暂时无法加载，请检查连接。");
    if (!block.isConnected || !input.checked || regionSelectionRequests.get(block) !== request) return;
    for (const region of envelope.data.regions) {
      const label = document.createElement("label"); label.className = "region-choice";
      const checkbox = document.createElement("input"); checkbox.className = "form-check-input"; checkbox.type = "checkbox"; checkbox.name = "targets"; checkbox.value = `${input.value}/${region.code}`; checkbox.disabled = !region.selectable; checkbox.checked = region.selectable && selected.has(checkbox.value);
      const caption = document.createElement("span"); caption.className = "region-choice-title"; caption.textContent = awsRegionNames[region.code] ? `${awsRegionNames[region.code]} · ${region.code}` : region.code;
      const state = document.createElement("span"); state.className = "region-choice-state"; state.textContent = awsRegionState(region.state);
      label.append(checkbox,caption,state);choices.append(label);
    }
    all.disabled = !envelope.data.regions.some(region => region.selectable);
    status.textContent = `已加载 ${envelope.data.regions.length} 个区域 · ${observationStatus(envelope.data)}。`;
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
  if (reload) loadSelectableRegions(reload.closest("[data-region-connection]"), true);
  if (all) {
    const fields = [...all.closest("[data-region-directory]").querySelectorAll('[name="targets"]:not(:disabled)')];
    const checked = !fields.every(field => field.checked); fields.forEach(field => {field.checked = checked;});
  }
});
document.addEventListener("DOMContentLoaded",configureRegionSelection);
document.addEventListener("htmx:afterSwap",configureRegionSelection);

const launchRegionRequests = new WeakMap();
async function loadLaunchRegions(row, refresh = false) {
  launchRegionRequests.get(row)?.abort();
  const request = new AbortController(); launchRegionRequests.set(row,request);
  const connection = launchField(row,"connection_id").value;
  const region = launchField(row,"region"), previous = region.value;
  const status = row.querySelector("[data-launch-region-status]");
  region.replaceChildren(new Option(connection ? "正在加载区域…" : "先选择访问连接", ""));
  if (!connection) {status.textContent = "选择连接后列出 Lightsail 支持的区域。";return;}
  status.textContent = "正在查询可用区域…";
  try {
    const response = await fetch(`/api/v1/lightsail/regions?${new URLSearchParams({connection_id:connection,refresh:String(refresh)})}`,{credentials:"same-origin",signal:request.signal});
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
    if (region.value) loadLaunchCatalogue(row);
  } catch(error) {
    if(error.name!=="AbortError") {region.replaceChildren(new Option("区域加载失败", ""));status.textContent=error.message;}
  }
}
document.addEventListener("click",event=>{const button=event.target.closest("[data-reload-regions]");if(button)loadLaunchRegions(button.closest("[data-launch-group]"), true);});

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

const cloudRegionRequests = new WeakMap();
function cloudField(form,name){return form.querySelector(`[name="${name}"]`);}
async function loadCloudRegions(form){
 cloudRegionRequests.get(form)?.abort();const request=new AbortController();cloudRegionRequests.set(form,request);
 const connection=cloudField(form,"connection_id").value,region=cloudField(form,"region"),previous=region.value;
 const accountLabel=cloudField(form,"connection_id").selectedOptions[0]?.parentElement?.label||"";
 const status=form.querySelector("[data-cloud-region-status]");region.replaceChildren(new Option(connection?"正在加载区域…":"先选择连接",""));
 if(!connection){status.textContent="";return;}
 const path="/api/v1/quotas/regions";
 status.textContent=accountLabel?`${accountLabel} · 正在查询区域…`:"正在查询区域…";
 try{
  const response=await fetch(`${path}?${new URLSearchParams({connection_id:connection})}`,{credentials:"same-origin",signal:request.signal});
  if(response.status===401){window.location.assign("/login");return;}
  const envelope=await response.json();if(!response.ok)throw new Error(envelope.error?.message||"无法读取区域列表");
  if(!form.isConnected||cloudRegionRequests.get(form)!==request)return;
  region.replaceChildren(new Option("请选择区域",""));
  for(const value of envelope.data.items){const option=new Option(`${value.code} · ${awsRegionNames[value.code]||value.name||value.code}${value.available?"":" · "+awsRegionState(value.state)}`,value.code);option.disabled=!value.available;region.append(option);}
  if(envelope.data.items.some(value=>value.code===previous&&value.available))region.value=previous;
  status.textContent=accountLabel||"区域状态已更新。";

  if(form.matches("[data-ec2-form]")&&region.value)loadEC2(form);
 if(form.matches("[data-wavelength-form]")&&region.value)loadWavelength(form);
 }catch(error){if(error.name!=="AbortError"){status.textContent=error.message;region.replaceChildren(new Option("区域加载失败",""));}}
}
function configureCloudForms(){
 document.querySelectorAll("[data-cloud-region-picker]").forEach(form=>{
  if(form.dataset.cloudInitialized)return;form.dataset.cloudInitialized="true";

  if(cloudField(form,"connection_id").value)loadCloudRegions(form);
 });
}
document.addEventListener("change",event=>{
 const form=event.target.closest("[data-cloud-region-picker]");if(!form)return;
 if(["connection_id","service"].includes(event.target.name)){
  loadCloudRegions(form);
 }
});
document.addEventListener("DOMContentLoaded",configureCloudForms);
document.addEventListener("htmx:afterSwap",configureCloudForms);

const ec2Catalogues=new WeakMap();
function ec2Scope(form){return JSON.stringify([cloudField(form,"connection_id").value,cloudField(form,"region").value,cloudField(form,"wavelength_zone_id").value]);}
function ec2Options(form){
 const data=ec2Catalogues.get(form);if(!data)return;
 const image=data.images.find(v=>v.id===cloudField(form,"image_id").value);
 launchChoices(cloudField(form,"instance_type"),data.types.filter(v=>v.architectures.includes(image?.architecture)),v=>`${v.id} · ${v.vcpu} vCPU / ${(v.memory_mib/1024).toFixed(1)} GiB`,v=>v.id);
 const disk=cloudField(form,"disk_gb");disk.min=String(Math.max(8,image?.min_disk_gb||8));if(Number(disk.value)<Number(disk.min))disk.value=disk.min;
 const subnet=data.subnets.find(v=>v.id===cloudField(form,"subnet_id").value);
 const parents=(data.parent_subnets||[]).filter(v=>v.vpc_id===subnet?.vpc_id&&v.public_ipv4_route&&v.available_ips>0);
 launchChoices(cloudField(form,'ingress_subnet_id'),parents,v=>`${v.id} · ${v.zone}`,v=>v.id);
 const parentTypes=(data.parent_types||[]).filter(v=>v.architectures.includes(image?.architecture));const preferred=['t3.nano','t4g.nano','t3.micro','t4g.micro'];parentTypes.sort((a,b)=>(preferred.includes(a.id)?preferred.indexOf(a.id):99)-(preferred.includes(b.id)?preferred.indexOf(b.id):99)||a.vcpu-b.vcpu||a.id.localeCompare(b.id));
 launchChoices(cloudField(form,'ingress_type'),parentTypes,v=>`${v.id} · ${v.vcpu} vCPU / ${(v.memory_mib/1024).toFixed(1)} GiB`,v=>v.id);
 form.querySelector("[data-ec2-network-hint]").textContent=data.wavelength_zone?(subnet?`${subnet.vpc_id} · ${subnet.zone} · Carrier Gateway 默认路由：${subnet.public_ipv4_route?"有":"无"}。自动配置全放行安全组、Carrier IP 与父区域 EIP 入口。`:"此 Zone 没有可用子网，请先准备网络。"):subnet?`${subnet.vpc_id} · ${subnet.zone} · IPv6 前缀：${subnet.ipv6_cidr||'未配置'} · 公网路由 IPv4:${subnet.public_ipv4_route?'有':'无'} / IPv6:${subnet.public_ipv6_route?'有':'无'}。创建时自动配置全放行安全组。`:'此区域没有可用子网，请先准备网络。';
}
function setEC2View(form,view){
 form.dataset.ec2View=view;if(view==='create'||view==='network')cloudField(form,'action').value=view;
 configureEC2(form);if(view!=='instances')form.querySelector('[data-ec2-editor-title]').focus({preventScroll:true});else form.closest('#ec2-form').querySelector('[data-ec2-list] h2').focus({preventScroll:true});
}
function configureEC2(form){
 const view=form.dataset.ec2View||'instances',editing=view!=='instances',action=cloudField(form,'action').value,create=editing&&view==='create',network=editing&&view==='network',manage=editing&&view==='manage',root=cloudField(form,'login_method').value==='root_password';
 const workspace=form.closest('#ec2-form');form.querySelector('[data-ec2-editor]').hidden=!editing;workspace.querySelector('[data-ec2-list]').hidden=editing;
 workspace.querySelectorAll('.workspace-tabs [data-ec2-open]').forEach(button=>{if(button.dataset.ec2Open===view)button.setAttribute('aria-current','page');else button.removeAttribute('aria-current');});
 form.querySelector('[data-ec2-create]').hidden=!create;form.querySelector('[data-ec2-network]').hidden=!network;form.querySelector('[data-ec2-existing]').hidden=!manage;
 cloudField(form,'instance_id').disabled=!manage;cloudField(form,'instance_id').required=manage;cloudField(form,'management_action').disabled=!manage;cloudField(form,'management_action').required=manage;if(manage)cloudField(form,'management_action').value=action;
 for(const field of form.querySelectorAll('[data-ec2-network] input,[data-ec2-network] select'))field.disabled=!network;
 for(const field of form.querySelectorAll('[data-ec2-create] input,[data-ec2-create] select'))field.disabled=!create;
 for(const field of form.querySelectorAll('[data-ec2-password]'))field.hidden=!root;
 form.querySelector('[data-ec2-key]').hidden=root;
 for(const name of ['root_password','root_password_confirm']){const field=cloudField(form,name);field.disabled=!create||!root;field.required=create&&root;if(field.disabled){field.value='';field.setCustomValidity('');}}
 cloudField(form,'key_pair_name').disabled=!create||root;cloudField(form,'key_pair_name').required=create&&!root;
 const wavelength=!!cloudField(form,'wavelength_zone_id').value;
 for(const name of ['zone','ipv6']){const field=cloudField(form,name);field.closest('label').hidden=wavelength;if(wavelength){field.disabled=true;if(field.type==='checkbox')field.checked=false;}}
 form.querySelector('[data-ec2-ingress]').hidden=!wavelength;for(const name of ['ingress_subnet_id','ingress_type']){cloudField(form,name).disabled=!create||!wavelength;cloudField(form,name).required=create&&wavelength;}
 const ipv6Option=cloudField(form,'management_action').querySelector('[value="enable_ipv6"]');ipv6Option.disabled=wavelength;ipv6Option.hidden=wavelength;
 const title=network?(wavelength?'准备 Wavelength 网络':'准备双栈网络'):create?(wavelength?'创建 Wavelength 实例':'创建 EC2 实例'):'管理实例';form.querySelector('[data-ec2-editor-title]').textContent=title;
 const hints={terminate:'终止不可撤销。预览会列出随实例删除的卷、配套入口和自动释放的固定 IP。',stop:'停止会中断服务，磁盘和保留的公网 IPv4 仍可能计费。',reboot:'任务确认 AWS 接受重启请求，系统是否恢复需另行核对。',enable_ipv6:'为主网卡添加 IPv6，需要现有子网、路由和安全组支持。'};
 form.querySelector('[data-ec2-action-hint]').textContent=manage?(hints[action]||''):'';form.querySelector('[data-ec2-action-notice]').hidden=!manage||!hints[action];form.querySelector('[data-ec2-action-hint]').classList.toggle('alert',action==='terminate');form.querySelector('[data-ec2-action-hint]').classList.toggle('alert-danger',action==='terminate');
 const submit=form.querySelector('[data-ec2-submit]');submit.disabled=!editing;submit.textContent=create?'预览创建配置':network?'预览网络配置':action==='terminate'?'预览终止操作':'预览实例操作';submit.classList.toggle('btn-danger',action==='terminate');submit.classList.toggle('btn-primary',action!=='terminate');
}
function ec2StateLabel(value){return {running:'运行中',stopped:'已停止',pending:'启动中',stopping:'停止中','shutting-down':'终止中',terminated:'已终止'}[value]||value;}
function renderEC2Instances(form,rows){
 rows=rows.filter(v=>!v.ingress_for);
 const table=document.createElement('table');table.className='table table-vcenter card-table resource-table';const head=document.createElement('thead'),tr=document.createElement('tr');for(const name of ['实例','状态','公网地址','操作']){const th=document.createElement('th');th.textContent=name;tr.append(th);}head.append(tr);table.append(head);const body=document.createElement('tbody');
 for(const v of rows){const row=document.createElement('tr');const identity=document.createElement('td'),name=document.createElement('strong'),meta=document.createElement('div');name.textContent=v.name;meta.textContent=`${v.id} · ${v.type}`;meta.className='data-id mt-1';identity.append(name,meta);
  const details=document.createElement('details');details.className='supporting-details mt-1';const summary=document.createElement('summary');summary.textContent='配置详情';const text=document.createElement('p');text.className='text-break';text.textContent=`AMI ${v.image_id} · VPC ${v.vpc_id} · 子网 ${v.subnet_id} · 安全组 ${v.security_group_ids.join(', ')} · 根卷 ${v.root_volume_id||'未知'} · 随终止删除：${v.delete_root_on_termination?'是':'否'}`;details.append(summary,text);identity.append(details);row.append(identity);
  const status=document.createElement('td'),badge=document.createElement('span');badge.className=`badge ${v.state==='running'?'bg-green-lt':'bg-secondary-lt'}`;badge.textContent=ec2StateLabel(v.state);status.append(badge);row.append(status);
  const ip=document.createElement('td');ip.className='data-id';const ipv4=document.createElement('div');ipv4.textContent=v.ingress?`入站 EIP ${v.ingress.public_ip||'绑定中'}`:v.carrier_ip?`出站 Carrier IP ${v.carrier_ip}`:(v.public_ip||'无公网 IPv4');if(v.ingress){const carrier=document.createElement('div');carrier.textContent=`出站 Carrier IP ${v.carrier_ip||'绑定中'}`;ip.append(carrier);const companion=document.createElement('div');companion.className='small text-secondary mt-1';companion.textContent=`入口 ${v.ingress.id} · ${ec2StateLabel(v.ingress.state)}`;details.append(companion);}const ipv6=document.createElement('div');ipv6.className='mt-1';ipv6.textContent=v.ipv6.join(' / ')||'无 IPv6';ip.prepend(ipv4);if(!v.zone.includes("-wl"))ip.append(ipv6);row.append(ip);
  const actions=document.createElement('td'),controls=document.createElement('div');controls.className='instance-actions';const manage=document.createElement('button');manage.type='button';manage.className='btn btn-sm';manage.textContent='管理';manage.disabled=['terminated','shutting-down'].includes(v.state);manage.addEventListener('click',()=>{cloudField(form,'instance_id').value=v.id;cloudField(form,'action').value='';setEC2View(form,'manage');});controls.append(manage);
  const ipLink=(id,service,label,zone='')=>{const a=document.createElement('a');a.className='btn btn-sm';a.textContent=label;a.href=`/ec2/instances/${encodeURIComponent(id)}/ip?${new URLSearchParams({connection_id:cloudField(form,'connection_id').value,region:cloudField(form,'region').value,service,wavelength_zone_id:zone})}`;return a;};
  if(!['terminated','shutting-down'].includes(v.state)){const wavelength=!!(v.carrier_ip||v.zone.includes('-wl'));if(v.ingress){const chooser=document.createElement('details');chooser.className='ip-actions';const summary=document.createElement('summary');summary.className='btn btn-sm';summary.textContent='更换 IP';const options=document.createElement('div');options.className='ip-options';options.append(ipLink(v.ingress.id,'ec2','更换入站 EIP'),ipLink(v.id,'wavelength','更换出站 Carrier IP',cloudField(form,'wavelength_zone_id').value));chooser.append(summary,options);controls.append(chooser);}else{controls.append(ipLink(v.id,wavelength?'wavelength':'ec2','更换 IP',cloudField(form,'wavelength_zone_id').value));}}
  actions.append(controls);row.append(actions);body.append(row);
 }
 if(!rows.length){const row=document.createElement('tr'),cell=document.createElement('td');cell.colSpan=4;cell.className='text-secondary text-center py-5';cell.textContent='此区域暂无 EC2 实例，可以创建一个新实例。';row.append(cell);body.append(row);}table.append(body);form.closest('#ec2-form').querySelector('[data-ec2-table]').replaceChildren(table);form.closest('#ec2-form').querySelector('[data-ec2-count]').textContent=`(${rows.length})`;
}
async function loadEC2(form, refresh = false){
 const connection=cloudField(form,"connection_id").value,region=cloudField(form,"region").value;if(!connection||!region)return;
 const scope=ec2Scope(form),generation=String(Number(form.dataset.ec2Generation||0)+1);form.dataset.ec2Generation=generation;
 const status=form.querySelector("[data-ec2-status]"),directory=form.querySelector("[data-ec2-catalogue-status]");
 status.textContent="正在更新实例…";directory.textContent=refresh?"正在从 AWS 更新创建配置…":"正在加载创建配置…";
 const current=()=>form.isConnected&&form.dataset.ec2Generation===generation&&ec2Scope(form)===scope;
 const query=new URLSearchParams({connection_id:connection,region,wavelength_zone_id:cloudField(form,"wavelength_zone_id").value,refresh:String(refresh)});
 const read=async part=>{const response=await fetch(`/api/v1/ec2/${part}?${query}`,{credentials:"same-origin"});if(response.status===401){window.location.assign('/login');throw new Error('请重新登录');}const result=await response.json();if(!response.ok)throw new Error(result.error?.message||'EC2 查询失败');return result.data;};
 await Promise.all([
  read("catalogue").then(catalogue=>{
   if(!current())return;ec2Catalogues.set(form,catalogue);
   launchChoices(cloudField(form,"zone"),catalogue.zones,v=>v);
   launchChoices(cloudField(form,"image_id"),catalogue.images,v=>`${v.name} · ${v.architecture} · ${v.id}`,v=>v.id);
   launchChoices(cloudField(form,"subnet_id"),catalogue.subnets,v=>`${v.name||v.id} · ${v.vpc_id} · ${v.zone}${catalogue.wavelength_zone?'':` · IPv6:${v.ipv6_cidr?'有':'无'}`}`,v=>v.id);
   launchChoices(cloudField(form,"key_pair_name"),catalogue.key_pairs,v=>v);
   ec2Options(form);configureEC2(form);directory.textContent=directoryStatus(catalogue);
  }).catch(error=>{if(current())directory.textContent=error.message;}),
  read("instances").then(listing=>{
   if(!current())return;launchChoices(cloudField(form,"instance_id"),listing.items.filter(v=>!v.ingress_for),v=>`${v.name} · ${v.id} · ${ec2StateLabel(v.state)}`,v=>v.id);
   renderEC2Instances(form,listing.items);configureEC2(form);status.textContent=`${listing.items.length} 个实例 · ${observationStatus(listing)}。`;
  }).catch(error=>{if(current())status.textContent=error.message;})
 ]);
}

function configureEC2Forms(){document.querySelectorAll("[data-ec2-form]").forEach(form=>{if(form.dataset.ec2Initialized)return;form.dataset.ec2Initialized="true";configureEC2(form);});}
document.addEventListener("change",event=>{const form=event.target.closest("[data-ec2-form]");if(!form)return;const name=event.target.name;if(["connection_id","region"].includes(name)){ec2Catalogues.delete(form);for(const name of ["root_password","root_password_confirm"]){cloudField(form,name).value="";cloudField(form,name).setCustomValidity("");}form.dataset.ec2Generation=String(Number(form.dataset.ec2Generation||0)+1);form.querySelector("[data-ec2-catalogue-status]").textContent="";form.closest("#ec2-form").querySelector("[data-ec2-table]").textContent="范围已变化，请重新加载。";if(name==="region")loadEC2(form);}else if(["image_id","subnet_id"].includes(name))ec2Options(form);else if(name==="management_action"){cloudField(form,"action").value=event.target.value;configureEC2(form);}else if(name==="login_method")configureEC2(form);});
document.addEventListener("input",event=>{const form=event.target.closest("[data-ec2-form]");if(!form||!["root_password","root_password_confirm"].includes(event.target.name))return;const password=cloudField(form,"root_password"),confirm=cloudField(form,"root_password_confirm");confirm.setCustomValidity(confirm.value&&confirm.value!==password.value?'两次密码不一致':'');});
document.addEventListener("click",event=>{const button=event.target.closest("[data-ec2-load]");if(button)loadEC2(button.closest("form"), true);});
document.addEventListener("submit",event=>{const form=event.target.closest("[data-ec2-form]");if(!form)return;if(form.dataset.ec2View==="instances"){event.preventDefault();event.stopImmediatePropagation();loadEC2(form);return;}if(ec2Catalogues.has(form))return;event.preventDefault();event.stopImmediatePropagation();form.querySelector("[data-ec2-status]").textContent="请先加载当前区域的实例与创建选项。";},true);
document.addEventListener("DOMContentLoaded",configureEC2Forms);document.addEventListener("htmx:afterSwap",configureEC2Forms);

function configureWorkspaceNavigation(){
 const toggle=document.querySelector('[data-nav-toggle]'),sidebar=document.getElementById('workspace-sidebar');
 if(!toggle||!sidebar||toggle.dataset.navReady)return;
 toggle.dataset.navReady='true';document.documentElement.classList.add('has-workspace-js');
 const setOpen=open=>{sidebar.classList.toggle('is-open',open);toggle.setAttribute('aria-expanded',String(open));toggle.setAttribute('aria-label',open?'收起导航':'打开导航');};
 toggle.addEventListener('click',()=>setOpen(!sidebar.classList.contains('is-open')));
 document.addEventListener('keydown',event=>{if(event.key==='Escape'&&sidebar.classList.contains('is-open')){setOpen(false);toggle.focus();}});
 window.matchMedia('(min-width: 992px)').addEventListener('change',()=>setOpen(false));
}
document.addEventListener('DOMContentLoaded',configureWorkspaceNavigation);

document.addEventListener('click',event=>{
 const ec2=event.target.closest('[data-ec2-open]');if(ec2){const form=ec2.closest('#ec2-form').querySelector('[data-ec2-form]');setEC2View(form,ec2.dataset.ec2Open);}
});

document.addEventListener('htmx:afterSwap',event=>{
 const root=event.detail.target;if(!root)return;
 const current=root.id?document.getElementById(root.id):root;
 if(current?.matches('#ec2-form,#address-form,#wavelength-workspace'))current.querySelector('[role="alert"][tabindex="-1"]')?.focus();
});

document.addEventListener('click',async event=>{
 const button=event.target.closest('[data-reload-account-proxies]');if(!button)return;
 const form=button.closest('form'),select=form.querySelector('[data-account-proxy]'),status=form.querySelector('[data-account-proxy-status]'),selected=select.value;button.disabled=true;status.textContent='正在读取代理列表…';
 try{const response=await fetch('/api/v1/proxies',{credentials:'same-origin'});const result=await response.json();if(!response.ok)throw new Error(result.error?.message||'代理列表暂不可用');select.replaceChildren(new Option('直连（不使用代理）',''));for(const proxy of result.data)select.append(new Option(`${proxy.name} · ${proxy.protocol==='http'?'HTTP':'SOCKS5'}${proxy.active?'':' · 已停用'}`,proxy.id));if(result.data.some(proxy=>proxy.id===selected))select.value=selected;else if(selected){select.append(new Option('原绑定代理当前不可用，请重新选择',selected));select.value=selected;}status.textContent='代理列表已更新。';}catch(error){status.textContent=error.message;}finally{button.disabled=false;}
});

const dashboardViewState={bedrockFilter:'',breakdownOpen:false,bedrockScroll:0};
function filterBedrock(){const field=document.querySelector('[data-bedrock-filter]');if(!field)return;field.value=dashboardViewState.bedrockFilter;const value=field.value.trim().toLowerCase();let matches=0;document.querySelectorAll('[data-bedrock-row]').forEach(row=>{row.hidden=!!value&&!row.textContent.toLowerCase().includes(value);if(!row.hidden)matches++;});const message=document.querySelector('[data-bedrock-no-match]');if(message)message.hidden=!value||matches>0;}
document.addEventListener('input',event=>{if(event.target.matches('[data-bedrock-filter]')){dashboardViewState.bedrockFilter=event.target.value;filterBedrock();}});
document.addEventListener('change',event=>{const form=event.target.closest('[data-dashboard-filter]');if(form&&event.target.matches('select'))form.requestSubmit();});
document.addEventListener('htmx:beforeSwap',event=>{if(event.detail.target?.id!=='dashboard-content')return;if(event.detail.xhr?.status>=400){event.detail.shouldSwap=false;document.querySelector('[data-dashboard-poll-error]')?.classList.remove('d-none');return;}dashboardViewState.breakdownOpen=!!document.querySelector('[data-quota-breakdown]')?.open;dashboardViewState.bedrockScroll=document.querySelector('[data-bedrock-table]')?.scrollTop||0;});
document.addEventListener('htmx:afterSwap',event=>{if(event.detail.target?.id!=='dashboard-content')return;filterBedrock();const details=document.querySelector('[data-quota-breakdown]');if(details)details.open=dashboardViewState.breakdownOpen;const table=document.querySelector('[data-bedrock-table]');if(table)table.scrollTop=dashboardViewState.bedrockScroll;});

function syncDashboardControls(){
 const content=document.getElementById('dashboard-content');if(!content)return;
 const ready=content.dataset.canRefresh==='true',button=document.querySelector('[data-dashboard-refresh]'),note=document.querySelector('[data-dashboard-no-connection]');if(button)button.disabled=!ready;if(note)note.hidden=ready;
 const form=document.querySelector('[data-dashboard-filter]');if(!form)return;
 for(const [name,selector] of [['account_id','[data-dashboard-account-options]'],['region','[data-dashboard-region-options]']]){const field=form.elements[name],source=document.querySelector(selector);if(!field||!source||document.activeElement===field)continue;const value=field.value;field.replaceChildren(...Array.from(source.content.querySelectorAll('option')).map(option=>option.cloneNode(true)));if(Array.from(field.options).some(option=>option.value===value))field.value=value;}
}
document.addEventListener('DOMContentLoaded',syncDashboardControls);document.addEventListener('htmx:afterSwap',syncDashboardControls);

const wavelengthRequests=new WeakMap();
async function loadWavelength(form, refresh = false){
 wavelengthRequests.get(form)?.abort();const request=new AbortController();wavelengthRequests.set(form,request);
 const connection=cloudField(form,'connection_id').value,region=cloudField(form,'region').value,status=form.querySelector('[data-wavelength-status]'),area=form.querySelector('[data-wavelength-table]');
 form.querySelector('[data-wavelength-submit]').disabled=true;area.textContent='正在加载…';if(!connection||!region){area.textContent='选择连接和父区域后加载。';return;}status.textContent='正在查询全部 Wavelength Zones…';
 try{
 const response=await fetch(`/api/v1/wavelength/zones?${new URLSearchParams({connection_id:connection,region,refresh:String(refresh)})}`,{credentials:'same-origin',signal:request.signal});if(response.status===401){location.assign('/login');return;}
 const result=await response.json();if(!response.ok)throw new Error(result.error?.message||'Wavelength 查询失败');if(!form.isConnected||wavelengthRequests.get(form)!==request)return;
 const table=document.createElement('table');table.className='table table-vcenter card-table resource-table';const head=document.createElement('thead'),header=document.createElement('tr');for(const name of ['Zone / Group','开通状态','网络边界组','管理']){const th=document.createElement('th');th.textContent=name;header.append(th);}head.append(header);table.append(head);const body=document.createElement('tbody'),groups=new Set();
 for(const z of result.data.zones){const row=document.createElement('tr'),identity=document.createElement('td'),name=document.createElement('strong');name.textContent=z.name;identity.append(name);const meta=document.createElement('div');meta.className='data-id mt-1';meta.textContent=`${z.id} · ${z.group}`;identity.append(meta);row.append(identity);
 const state=document.createElement('td'),badge=document.createElement('span');badge.className=`badge ${z.opt_in_status==='opted-in'?'bg-green-lt':'bg-secondary-lt'}`;badge.textContent=z.opt_in_status==='opted-in'?'已开通':z.opt_in_status==='not-opted-in'?'未开通':z.opt_in_status;state.append(badge);if(z.state!=='available'){const note=document.createElement('div');note.textContent=z.state;state.append(note);}row.append(state);
 const border=document.createElement('td');border.className='data-id';border.textContent=z.border_group;row.append(border);const actions=document.createElement('td');
 if(z.opt_in_status==='not-opted-in'&&!groups.has(z.group)){groups.add(z.group);const label=document.createElement('label');label.className='form-check';const check=document.createElement('input');check.type='checkbox';check.name='wavelength_zone_id';check.value=z.id;check.checked=(form.dataset.wavelengthSelected||'').trim().split(' ').includes(z.id);check.className='form-check-input';check.setAttribute('aria-label',`开通 ${z.group}`);const text=document.createElement('span');text.className='form-check-label';text.textContent='选择开通该组';label.append(check,text);actions.append(label);
 }else if(z.opt_in_status==='opted-in'&&z.state==='available'){const a=document.createElement('a');a.className='btn btn-sm';a.textContent='管理 Zone';a.href=`/ec2?${new URLSearchParams({connection_id:connection,region,wavelength_zone_id:z.id})}`;actions.append(a);}else{actions.textContent=z.opt_in_status==='not-opted-in'?'随同组开通':'暂不可用';}row.append(actions);body.append(row);
 }
 if(!result.data.zones.length){const row=document.createElement('tr'),cell=document.createElement('td');cell.colSpan=4;cell.className='text-secondary text-center py-5';cell.textContent='AWS 未返回该父区域的 Wavelength Zones。';row.append(cell);body.append(row);}table.append(body);area.replaceChildren(table);form.querySelector('[data-wavelength-submit]').disabled=!form.querySelector('[name="wavelength_zone_id"]:checked');status.textContent=`已更新 ${result.data.zones.length} 个 Zones · 父区域 ${region}`;
 }catch(error){if(error.name!=='AbortError'){status.textContent=error.message;area.textContent='查询未完成，请重试。';}}
}
document.addEventListener('click',event=>{const button=event.target.closest('[data-wavelength-load]');if(button)loadWavelength(button.closest('form'), true);});
document.addEventListener('change',event=>{const form=event.target.closest('[data-wavelength-form]');if(!form)return;if(event.target.name==='region'||event.target.name==='connection_id'){form.dataset.wavelengthSelected='';loadWavelength(form);}if(event.target.name==='wavelength_zone_id')form.querySelector('[data-wavelength-submit]').disabled=!form.querySelector('[name="wavelength_zone_id"]:checked');});
