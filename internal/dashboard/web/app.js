"use strict";

const byId = id => document.getElementById(id);
const number = new Intl.NumberFormat("zh-CN");
const date = new Intl.DateTimeFormat("zh-CN", {month:"2-digit", day:"2-digit", hour:"2-digit", minute:"2-digit", hour12:false});
const clock = new Intl.DateTimeFormat("zh-CN", {hour:"2-digit", minute:"2-digit", second:"2-digit", hour12:false});
const labels = new Map(Object.entries({
  aws_cloudwatch_alarm:"AWS CloudWatch 告警",aws_cloudwatch_insufficient_data:"AWS CloudWatch 采样不足",
  ip_frequency:"IP 请求频率",uri_frequency:"URI 请求频率", unexpected_host:"异常 Host",
  unexpected_user_agent:"异常 User-Agent",sensitive_uri:"敏感 URI",sql_injection:"SQL 注入特征",
  xss:"XSS 特征",path_traversal:"路径遍历特征",http_status:"HTTP 状态异常",latency_p95:"P95 延迟异常",correlated:"多维事件关联",
  backend_critical_log:"后端严重日志",backend_error_spike:"后端错误激增",
  backend_warning_spike:"后端警告激增",backend_business_error_spike:"业务错误码激增",
  backend_exception:"后端未处理异常",backend_dependency_failure:"依赖故障激增",
  nginx_sensitive_artifact:"敏感文件探测",nginx_unexpected_method:"异常请求方法",
  nginx_waf_block:"WAF 拦截",nginx_waf_category:"WAF 安全分类",nginx_legacy_tls:"旧版 TLS",
  nginx_upstream_error:"上游错误激增",nginx_route_error:"接口限流或上游错误",
  nginx_auth_failure_burst:"认证失败激增",nginx_not_found_burst:"404 激增",nginx_rate_limit_burst:"限流激增",scanner_vulnerability:"漏洞扫描发现"
}));
const countryNames = typeof Intl.DisplayNames==="function"?new Intl.DisplayNames(["zh-CN"],{type:"region"}):null;
const statuses = new Map([["done","已完成"],["failed","失败"],["running","运行中"]]);
let trend = [];
let activeRequest;

function locationLabel(event) {
  const country=event.Country||"";
  const label=countryNames&&/^[A-Z]{2}$/.test(country)?countryNames.of(country):country;
  return [label,event.Region].filter(Boolean).join(" / ")||"—";
}

function element(tag, text, style) {
  const el = document.createElement(tag);
  if (text !== undefined) el.textContent = text;
  if (style) el.className = style;
  return el;
}

function rows(body, records, columns, render) {
  body.replaceChildren();
  if (!records.length) {
    const cell = element("td", "当前筛选范围内暂无记录。", "empty");
    cell.colSpan = columns;
    body.append(element("tr"));
    body.firstChild.append(cell);
    return;
  }
  for (const record of records) {
    const row = element("tr");
    for (const value of render(record)) {
      const cell = element("td");
      cell.append(value);
      row.append(cell);
    }
    body.append(row);
  }
}

function showDetail(record) {
  const {event,analysis} = record;
  byId("detail-title").textContent = labels.get(event.Rule) || event.Rule;
  const fields = event.Rule==="scanner_vulnerability" ? [
    ["CVE",event.Target],["扫描记录数",number.format(event.Count)],["数据源",record.source],["Run ID",record.id]
  ] : [
    ["客户端 IP",event.IP],["地区",locationLabel(event)],
    ["目标标识",event.Target],[event.Rule.startsWith("backend_")?"日志条数":"请求次数",number.format(event.Count)],
    ["关联规则",(event.Signals || []).map(rule => labels.get(rule) || rule).join("、")],
    ["Run ID",record.id],["Correlation ID",record.correlation_id]
  ];
  const list = element("dl");
  for (const [name,value] of fields) list.append(element("dt",name),element("dd",value || "—"));
  const content = byId("detail-content");
  content.replaceChildren(list);
  if (/^[0-9a-f]{32}$/.test(record.id)) {
    const link=element("a","查看巡检报告","detail-link");
    link.href="/api/reports/"+encodeURIComponent(record.id);
    link.target="_blank";
    link.rel="noopener noreferrer";
    content.append(link);
  }
  content.append(element("h2","AI 分析"));
  if (analysis) {
    content.append(element("p",analysis.summary));
    const actions = element("ul");
    for (const action of analysis.actions || []) actions.append(element("li",action));
    if (actions.childElementCount) content.append(actions);
  } else {
    content.append(element("p","该任务暂无 AI 分析。"));
  }
  byId("detail").showModal();
}

function draw() {
  const canvas = byId("trend");
  canvas.hidden = !trend.length;
  byId("trend-empty").hidden = trend.length > 0;
  if (!trend.length) return;
  const {width,height} = canvas.getBoundingClientRect();
  if (!width || !height) return;
  const scale = window.devicePixelRatio || 1;
  canvas.width = width * scale;
  canvas.height = height * scale;
  const ctx = canvas.getContext("2d");
  ctx.scale(scale,scale);
  ctx.font = "12px system-ui";
  const peak = Math.max(1,...trend.map(entry => entry.count));
  const compact = new Intl.NumberFormat("zh-CN",{notation:"compact",maximumFractionDigits:1});
  const left = Math.max(43,ctx.measureText(compact.format(peak)).width + 12);
  const right = width - 9, bottom = height - 31, top = 13;
  ctx.strokeStyle = "#e2eaf6";
  ctx.fillStyle = "#526885";
  ctx.textAlign = "right";
  for (let step=0;step<=4;step++) {
    const y = bottom - step * (bottom-top)/4;
    ctx.beginPath();ctx.moveTo(left,y);ctx.lineTo(right,y);ctx.stroke();
    ctx.fillText(compact.format(peak*step/4),left-8,y+4);
  }
  const first = Date.parse(trend[0].at), last = Date.parse(trend.at(-1).at);
  const points = trend.map(entry => [
    first===last?(left+right)/2:left+(right-left)*(Date.parse(entry.at)-first)/(last-first),
    bottom-(bottom-top)*entry.count/peak
  ]);
  const gradient = ctx.createLinearGradient(0,top,0,bottom);
  gradient.addColorStop(0,"#386deb44");
  gradient.addColorStop(1,"#386deb05");
  ctx.beginPath();
  ctx.moveTo(points[0][0],bottom);
  for (const [x,y] of points) ctx.lineTo(x,y);
  ctx.lineTo(points.at(-1)[0],bottom);
  ctx.fillStyle=gradient;ctx.fill();
  ctx.beginPath();
  points.forEach(([x,y],index)=>index?ctx.lineTo(x,y):ctx.moveTo(x,y));
  ctx.strokeStyle="#3164e4";ctx.lineWidth=2.5;ctx.lineJoin="round";ctx.stroke();
  ctx.fillStyle="#3164e4";
  if (points.length<=24) for (const [x,y] of points) {
    ctx.beginPath();ctx.arc(x,y,3,0,2*Math.PI);ctx.fill();
  }
  ctx.fillStyle="#526885";
  ctx.textAlign="left";
  ctx.fillText(date.format(first),left,bottom+22);
  if (first!==last && right-left>145) {
    ctx.textAlign="right";
    ctx.fillText(date.format(last),right,bottom+22);
  }
  canvas.setAttribute("aria-label",`请求趋势，${trend.length} 个窗口，峰值 ${number.format(peak)}`);
}

function render(data) {
  for (const key of ["requests","alerts","runs","failed"]) byId(key).textContent = number.format(data[key]);
  byId("interval").textContent = data.kind==="hour"?"按小时聚合":"每 5 分钟聚合";
  byId("last").textContent = "更新于 " + clock.format(new Date(data.updated));
  const source = byId("source"), previous = source.value;
  const sources = new Set(data.sources);
  if (previous) sources.add(previous);
  source.replaceChildren(new Option("全部数据源",""), ...Array.from(sources, name => new Option(name,name)));
  source.value=previous;
  trend=data.trend;
  draw();

  const rules = byId("rules");
  rules.replaceChildren();
  for (const rule of data.rules) {
    const name = labels.get(rule.rule) || rule.rule;
    const line = element("div",undefined,"bar-row"), caption=element("div",undefined,"bar-label");
    caption.append(element("span",name),element("strong",number.format(rule.count)));
    const bar = element("progress");
    bar.max=data.rules[0].count;
    bar.value=rule.count;
    bar.setAttribute("aria-label",name);
    line.append(caption,bar);
    rules.append(line);
  }
  if (!data.rules.length) rules.append(element("p","当前筛选范围内暂无规则命中。","empty"));

  rows(byId("event-body"),data.events,7,item => {
    const event=item.event;
    return [labels.get(event.Rule)||event.Rule,riskRating(item.score),element("span",event.IP||"—","mono"),locationLabel(event),item.source,date.format(new Date(item.at)),detailButton(item)];
  });
  rows(byId("task-body"),data.tasks,5,item=>{
    const report=element("a",item.id,"mono");
    report.href="/api/reports/"+encodeURIComponent(item.id);
    report.title="查看巡检报告";
    return [item.source,element("span",statuses.get(item.status)||item.status,"pill "+(statuses.has(item.status)?item.status:"")),
      date.format(new Date(item.window_end)),String(item.attempts),report];
  });
}

function riskRating(score) {
  return element("span",String(score),"pill "+(score>=80?"high":score>=65?"medium":""));
}

function detailButton(item) {
  const button=element("button","查看","detail-link");
  button.type="button";
  button.addEventListener("click",()=>showDetail(item));
  return button;
}

function renderVulnerabilities(data) {
  const total=number.format(data.total);
  byId("vulnerability-status").textContent=data.total>data.events.length
    ? `共 ${total} 条发现，显示最近 ${data.events.length} 条。` : `共 ${total} 条发现。`;
  rows(byId("vulnerability-body"),data.events,5,item=>[
    element("span",item.event.Target,"mono"),number.format(item.event.Count),
    item.source,date.format(new Date(item.at)),detailButton(item)
  ]);
}

async function refreshVulnerabilities(filters,controller) {
  byId("vulnerability-status").textContent="正在加载漏洞发现…";
  byId("vulnerability-body").replaceChildren();
  try {
    const response=await fetch("/api/vulnerabilities?"+filters,{signal:controller.signal});
    if (!response.ok) throw new Error(String(response.status));
    const data=await response.json();
    if (activeRequest===controller) renderVulnerabilities(data);
  } catch (error) {
    if (error.name!=="AbortError" && activeRequest===controller) byId("vulnerability-status").textContent="漏洞发现暂不可用，请刷新重试。";
  }
}

function renderNginxRoutes(data) {
  byId("nginx-status").textContent=`共 ${number.format(data.total)} 条事件，显示最近 ${data.events.length} 条。`;
  rows(byId("nginx-body"),data.events,9,item=>[
    element("span",item.event.Target||"—","mono"),labels.get(item.event.Rule)||item.event.Rule,
    riskRating(item.score),element("span",item.event.IP||"—","mono"),locationLabel(item.event),number.format(item.event.Count),item.source,date.format(new Date(item.at)),detailButton(item)
  ]);
}

async function refreshNginxRoutes(filters,controller) {
  byId("nginx-status").textContent="正在加载巡检事件…";
  byId("nginx-body").replaceChildren();
  try {
    const response=await fetch("/api/nginx-risk-routes?"+filters,{signal:controller.signal});
    if (!response.ok) throw new Error(String(response.status));
    const data=await response.json();
    if (activeRequest===controller) renderNginxRoutes(data);
  } catch (error) {
    if (error.name!=="AbortError" && activeRequest===controller) byId("nginx-status").textContent="巡检事件暂不可用，请刷新重试。";
  }
}

async function checkSource(name,button,status) {
  const codes={timeout:"查询超时",authorization_denied:"权限不足",endpoint_missing:"接口不存在",upstream_busy:"上游繁忙",query_failed:"查询失败"};
  button.disabled=true;
  status.textContent="正在查询…";
  try {
    const response=await fetch("/api/sources/"+encodeURIComponent(name)+"/check",{
      method:"POST",headers:{"X-SRE-AIObs-Check":"1"}
    });
    if (!response.ok) throw new Error(String(response.status));
    const result=await response.json();
    status.textContent=result.status==="ok"
      ? (result.count===undefined?`AWS 连接正常 · ${result.duration_ms} ms`:`可查询 · 过去 1 分钟 ${number.format(result.count)} 条 · ${result.duration_ms} ms`)
      : (codes[result.code]||"查询失败");
  } catch(error) {
    status.textContent=error.message==="429"?"检查繁忙，请稍后重试。":"无法完成检查。";
  } finally {
    button.disabled=false;
  }
}

function renderSources(sources) {
  byId("source-status").textContent=`已配置 ${sources.length} 个数据源。`;
  rows(byId("source-body"),sources,5,source=>{
    const cell=element("span",undefined,"source-check");
    const status=element("span");
    status.setAttribute("role","status");
    const check=element("button","检查", "detail-link");
    check.type="button";
    check.setAttribute("aria-label","检查 "+source.name);
    check.addEventListener("click",()=>checkSource(source.name,check,status));
    cell.append(check,status);
    const capabilities=[source.backend?"后端日志":"",source.vulnerabilities?"漏洞扫描":""].filter(Boolean);
    return [source.name,source.kind==="aws"?"AWS CloudWatch":"Elasticsearch",source.environment,capabilities.join("、")||"—",cell];
  });
}

async function loadSources() {
  try {
    const response=await fetch("/api/sources");
    if (!response.ok) throw new Error(String(response.status));
    const sources=await response.json();
    renderSources(sources);
  } catch {
    byId("source-status").textContent="数据源列表暂不可用，请重新打开页面。";
  }
}

async function refresh() {
  activeRequest?.abort();
  const controller = new AbortController();
  activeRequest = controller;
  const connection=byId("connection");
  connection.textContent="更新中";
  connection.parentElement.dataset.state="loading";
  byId("refresh").disabled=true;
  byId("error").hidden=true;
  const filters=new URLSearchParams(Object.fromEntries(["source","hours","score"].map(id=>[id,byId(id).value])));
  void refreshVulnerabilities(filters,controller);
  void refreshNginxRoutes(filters,controller);
  try {
    const response=await fetch("/api/dashboard?"+filters,{signal:controller.signal});
    const login=response.headers.get("X-SRE-AIObs-Login") === "/login";
    if (login && response.status === 401) {
      window.location.replace("/login");
      return;
    }
    byId("logout").hidden = !login;
    if (!response.ok) throw new Error(String(response.status));
    const data=await response.json();
    if (activeRequest!==controller) return;
    render(data);
    connection.textContent="已连接";
    connection.parentElement.dataset.state="connected";
  } catch (error) {
    if (error.name==="AbortError" || activeRequest!==controller) return;
    connection.textContent="数据不可用";
    connection.parentElement.dataset.state="error";
    byId("error").textContent=byId("last").textContent?"刷新失败，当前显示上次成功加载的数据。":"暂时无法加载数据，请稍后刷新。";
    byId("error").hidden=false;
  } finally {
    if (activeRequest===controller) byId("refresh").disabled=false;
  }
}

for (const id of ["source","hours","score"]) byId(id).addEventListener("change",refresh);
document.querySelector(".filters").addEventListener("submit",event=>{event.preventDefault();refresh();});
byId("refresh").addEventListener("click",refresh);
new ResizeObserver(draw).observe(byId("trend").parentElement);
window.addEventListener("hashchange",()=>{
  for (const item of document.querySelectorAll("nav a")) {
    if (item.hash===(location.hash||"#overview")) item.setAttribute("aria-current","location");
    else item.removeAttribute("aria-current");
  }
});
refresh();

async function loadAlertSettings() {
  const status=byId("alert-status");
  try {
    const response=await fetch("/api/alert-settings");
    if (!response.ok) throw new Error(String(response.status));
    const settings=await response.json();
    byId("minimum-score").value=settings.minimum_score;
    byId("cooldown-minutes").value=settings.cooldown_minutes;
    for (const key of ["slack","telegram","email"]) byId("alert-"+key).checked=settings[key];
    byId("slack-state").textContent=settings.slack_configured?"· 已注入 Webhook":"· 未配置 Webhook";
    byId("telegram-state").textContent=settings.telegram_configured?"· 已配置机器人与会话":"· 缺少机器人或会话";
    const email=settings.email_destination||{};
    byId("email-from").value=email.from||"";
    byId("email-to").value=(email.to||[]).join("\n");
    byId("email-region").value=email.region||"";
    showEmailDestinationStatus(settings.email_configured);
    byId("alert-fields").disabled=!settings.editable;
    byId("alert-actions").hidden=!settings.editable;
    status.textContent=settings.editable?"保存后应用于后续告警任务。":"请配置告警管理密码以启用修改。";
  } catch {
    status.textContent="暂时无法加载告警设置，请刷新页面。";
  }
}

function showEmailDestinationStatus(configured) {
  byId("email-state").textContent=configured?"· 已保存发件人与收件人":"· 缺少发件人或收件人";
}

byId("alert-form").addEventListener("submit",async event=>{
  event.preventDefault();
  const button=byId("alert-save"), password=byId("alert-password"), status=byId("alert-status");
  button.disabled=true;
  status.textContent="正在保存…";
  const settings={
    minimum_score:byId("minimum-score").valueAsNumber,
    cooldown_minutes:byId("cooldown-minutes").valueAsNumber,
    email_destination:{
      from:byId("email-from").value.trim(),
      to:byId("email-to").value.split(/[,\n]+/).map(address=>address.trim()).filter(Boolean),
      region:byId("email-region").value.trim()
    }
  };
  for (const key of ["slack","telegram","email"]) settings[key]=byId("alert-"+key).checked;
  try {
    const response=await fetch("/api/alert-settings",{
      method:"PUT",credentials:"same-origin",
      headers:{"Content-Type":"application/json","X-Alert-Settings-Password":password.value},
      body:JSON.stringify(settings)
    });
    if (response.status===403) status.textContent="告警管理密码错误或未配置。";
    else if (response.status===400) status.textContent="请检查风险阈值、邮件地址和 AWS 区域，收件地址最多 50 个。";
    else if (!response.ok) status.textContent="保存失败，请检查设置后重试。";
    else {
      showEmailDestinationStatus(Boolean(settings.email_destination.from&&settings.email_destination.to.length));
      status.textContent="已保存。后续告警任务将使用新设置。";
    }
  } catch {
    status.textContent="无法保存，请检查连接后重试。";
  } finally {
    password.value="";
    button.disabled=false;
  }
});

loadAlertSettings();
loadSources();
