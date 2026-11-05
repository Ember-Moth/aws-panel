---
version: alpha
name: AWS Panel Workspace
description: A calm, compact cloud operations workspace with neutral surfaces, blue actions, grouped navigation, list-plus-detail workspaces, and preview-first change flows.
colors:
  primary: "#2457D6"
  primary-hover: "#1D46AD"
  on-primary: "#FFFFFF"
  background: "#F6F7F9"
  surface: "#FFFFFF"
  sidebar: "#F0F2F5"
  text: "#161B22"
  muted: "#56606D"
  border: "#E3E6EB"
  button-border: "#CDD3DB"
  input-border: "#89929F"
  active: "#EBF0FD"
  active-text: "#1F4BC2"
  success: "#17663F"
  success-surface: "#E7F5EC"
  warning: "#7F4D00"
  warning-surface: "#FFF3D6"
  danger: "#B42318"
  danger-surface: "#FDEEEB"
typography:
  metric:
    fontFamily: "system-ui, -apple-system, BlinkMacSystemFont, Segoe UI, PingFang SC, Microsoft YaHei, sans-serif"
    fontSize: 32px
    fontWeight: 600
    lineHeight: 1.2
    letterSpacing: -0.02em
  metric-mobile:
    fontFamily: "{typography.metric.fontFamily}"
    fontSize: 24px
    fontWeight: 600
    lineHeight: 1.2
    letterSpacing: -0.02em
  metric-label:
    fontFamily: "{typography.metric.fontFamily}"
    fontSize: 13px
    fontWeight: 500
    lineHeight: 1.5
    letterSpacing: 0px
  heading:
    fontFamily: "system-ui, -apple-system, BlinkMacSystemFont, Segoe UI, PingFang SC, Microsoft YaHei, sans-serif"
    fontSize: 24px
    fontWeight: 600
    lineHeight: 1.3
    letterSpacing: -0.02em
  section:
    fontFamily: "{typography.heading.fontFamily}"
    fontSize: 16px
    fontWeight: 600
    lineHeight: 1.5
    letterSpacing: 0px
  body:
    fontFamily: "{typography.heading.fontFamily}"
    fontSize: 14px
    fontWeight: 400
    lineHeight: 1.6
    letterSpacing: 0px
  navigation:
    fontFamily: "{typography.heading.fontFamily}"
    fontSize: 13px
    fontWeight: 400
    lineHeight: 1.5
    letterSpacing: 0px
  form-section:
    fontFamily: "{typography.heading.fontFamily}"
    fontSize: 14px
    fontWeight: 600
    lineHeight: 1.5
    letterSpacing: 0px
  mobile-input:
    fontFamily: "{typography.heading.fontFamily}"
    fontSize: 16px
    fontWeight: 400
    lineHeight: 1.6
    letterSpacing: 0px
  label:
    fontFamily: "{typography.heading.fontFamily}"
    fontSize: 13px
    fontWeight: 600
    lineHeight: 1.5
    letterSpacing: 0px
  caption:
    fontFamily: "{typography.heading.fontFamily}"
    fontSize: 12px
    fontWeight: 400
    lineHeight: 1.5
    letterSpacing: 0px
  code:
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace"
    fontSize: 12px
    fontWeight: 400
    lineHeight: 1.5
    letterSpacing: 0px
rounded:
  sm: 5px
  control: 8px
  panel: 10px
  pill: 999px
spacing:
  xs: 4px
  sm: 8px
  md: 12px
  lg: 16px
  panel: 20px
  section: 24px
  page: 32px
components:
  metric-number:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.text}"
    typography: "{typography.metric}"
  metric-number-mobile:
    typography: "{typography.metric-mobile}"
  metric-label:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.muted}"
    typography: "{typography.metric-label}"
  separator:
    backgroundColor: "{colors.border}"
    height: 1px
  input-boundary:
    backgroundColor: "{colors.input-border}"
    width: 1px
  page:
    backgroundColor: "{colors.background}"
    textColor: "{colors.text}"
    typography: "{typography.body}"
  sidebar:
    backgroundColor: "{colors.sidebar}"
    textColor: "{colors.text}"
    typography: "{typography.navigation}"
    width: 232px
  navigation-active:
    backgroundColor: "{colors.active}"
    textColor: "{colors.active-text}"
    typography: "{typography.label}"
    rounded: "{rounded.control}"
  panel:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.text}"
    rounded: "{rounded.panel}"
    padding: "{spacing.panel}"
  input:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.text}"
    typography: "{typography.body}"
    rounded: "{rounded.control}"
    height: 36px
  mobile-input:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.text}"
    typography: "{typography.mobile-input}"
    height: 44px
  resource-table:
    width: 620px
  primary-button:
    backgroundColor: "{colors.primary}"
    textColor: "{colors.on-primary}"
    typography: "{typography.label}"
    rounded: "{rounded.control}"
    height: 36px
  primary-button-hover:
    backgroundColor: "{colors.primary-hover}"
    textColor: "{colors.on-primary}"
  form-section-heading:
    textColor: "{colors.text}"
    typography: "{typography.form-section}"
  supporting-text:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.muted}"
    typography: "{typography.caption}"
  status-success:
    backgroundColor: "{colors.success-surface}"
    textColor: "{colors.success}"
    typography: "{typography.caption}"
  status-warning:
    backgroundColor: "{colors.warning-surface}"
    textColor: "{colors.warning}"
    typography: "{typography.caption}"
  status-danger:
    backgroundColor: "{colors.danger-surface}"
    textColor: "{colors.danger}"
    typography: "{typography.caption}"
---

# AWS Panel Design System

## Overview

面向中文小团队的日常 AWS 运维。视觉基调是安静、清晰、紧凑：浅色平面、细分隔线、适中的数据密度和克制的蓝色。页面首先回答“我在管理哪类资源、哪个账号、哪个区域”，随后才展示当前流程需要的操作。

视觉方向参考 [VoltAgent 的 Supabase 设计分析](https://github.com/VoltAgent/awesome-design-md/blob/main/design-md/supabase/DESIGN.md)（第三方分析，非官方规范）：中性表面、克制的强调色、细边界与小圆角。产品导航、列表与详情并排、操作与列表分离来自本项目的工作流程；不复制参考品牌、绿色主题、专有字体、图片或文案。

本文件为视觉契约，`web/static/app.css` 是对应实现。保留 Go template、htmx 与已有 Tabler 基础组件，不引入新的 UI 框架、外部 CDN 或装饰性资产。只提供浅色主题；增加深色主题前需要先定义完整的语义色并覆盖全部状态。

## Colors

`{colors.background}` 承载页面，`{colors.surface}` 承载主要数据和表单，`{colors.sidebar}` 区分全局导航。边框 `{colors.border}` 只划分内容，输入边界使用 `{colors.input-border}`。正文使用 `{colors.text}`，说明和元数据使用 `{colors.muted}`。

蓝色 `{colors.primary}` 只用于主操作、链接和焦点，悬停变为 `{colors.primary-hover}`。选中导航使用 `{colors.active}` 与 `{colors.active-text}`。成功、警告、危险分别使用对应语义色及浅底，并同时显示文字。普通只读/预览模式用中性说明，不把所有页面都做成警报页。

正文对其背景至少达到 WCAG AA 4.5:1。焦点轮廓和功能控件边界至少达到 3:1。不要用降低整个元素透明度的方式制作低对比辅助文案。不可用控件保留可辨认文字和原因。

## Typography

使用系统字体栈，优先系统 UI 字体，中文由 PingFang SC / Microsoft YaHei / 系统 sans-serif 回退覆盖，无需下载字体。页标题使用 `{typography.heading}`，分区标题使用 `{typography.section}`，正文使用 `{typography.body}`，表单分组使用 `{typography.form-section}`，导航使用 `{typography.navigation}`，字段/按钮使用 `{typography.label}`，次要信息使用 `{typography.caption}`。

ID、IP、ARN 和技术代码使用 `{typography.code}`；日期、容量和数量采用等宽数字。移动表单使用 `{typography.mobile-input}` 的 16px，避免触控浏览器聚焦输入时自动放大。中文不全大写、不增加装饰性字距。标题不过度放大，长 ARN 允许换行或通过详情查看，不能撑破页面。

## Layout

桌面使用 232px 固定侧栏、56px 顶部上下文栏和最大 1440px 内容区域。正文左右留 `{spacing.page}`；页标题与主体之间用 `{spacing.section}`。所有布局以 4px 为基本节奏。卡片内边距 `{spacing.panel}`，相关字段间距 `{spacing.lg}`，分区之间 `{spacing.section}`。

导航顶部固定“概览 / 实例 / 任务”三项，其后按“资源 / 管理”分组，按角色显示入口。任务、资源、凭据配置不能混为一层无分组的长顶栏。实时实例和缓存资源快照必须在页面语义上区分。

资源管理默认展示账号/区域筛选及资源列表。实例、任务采用“列表 + 详情”工作区：选中行在同一页右侧展示详情与上下文操作，详情面板在页面流内（窄屏排到列表下方），不是覆盖层或抽屉。创建与网络准备是单独的工作视图，生命周期和地址变更由详情面板进入既有的预览流程；返回列表保留当前查询范围。不得把完整创建表单固定堆在资源表格上方。每个视图只突出一个主操作；其他操作为中性按钮或上下文入口。

创建表单按身份与规格、网络、登录方式分组，保留可见的账号和区域。与当前操作无关的字段隐藏且禁用，避免浏览器校验、错误提交或秘密残留。预览页保留费用、配额和不可逆后果。

991px 及以下将侧栏变成可展开的页内导航，不使用需要焦点陷阱的覆盖层；内容左右留 20px。767px 及以下表单单列，按钮与菜单触控目标至少 44px，实时资源表格最小宽度为 `{components.resource-table.width}`，只在自身容器内横向滚动，IPv4 不拆行。320px 宽度下页面整体不得横向溢出。

## Elevation & Depth

默认平面。主要面板以白底、1px 内容边界和 `{rounded.panel}` 建立层级；卡片和按钮不使用渐变或常驻投影。表单分组使用间距和分隔线，不再一层套一层卡片。数据详情用原生折叠内容，不用装饰性浮动面板。

## Shapes

输入和按钮用 `{rounded.control}`，面板用 `{rounded.panel}`，状态标签用 `{rounded.sm}`。图标为 18px 中性线性 SVG，1.7px 描边，装饰图标标记 aria-hidden；图标不能替代操作名称。不引入品牌图标库、插画或背景纹理。

## Components

- **应用外壳**：侧栏有稳定分组、当前页标记（白底、细边框、蓝色图标）与文本标签；顶部显示当前页面、全局“预览模式”标记（云端写入关闭时）和成员菜单（个人设置、退出）。各页面不再重复预览模式提示。提供跳到主内容的键盘链接。移动导航按钮暴露 aria-expanded，Escape 收起并返回按钮焦点。
- **快照时效**：实例、地址、区域和配额页面明确标注读取时间、缓存/后台刷新状态，并提供“刷新最新”。配额上限与资源用量分开标注时间，不能把较早的上限描述为刚刚查询。
- **目录加载**：选择账号与区域后自动显示创建选项，复用服务端已有目录；显示数据更新时间和后台更新状态，提供中性“刷新最新”按钮。实例实时查询与创建目录独立呈现，不能让实例查询延迟阻塞镜像与规格选择。手动刷新保留有效选择与已填输入。
- **资源范围栏**：连接、区域和必要的服务选项保持在操作上方。选择后显示账号名称和 AWS ID，区域代码置于名称之前，避免窄屏截断关键范围。加载按钮为中性，状态通过 role=status 更新。未知、未加载、无资源与查询失败分别表达；不把失败渲染为零资源。
- **资源表格**：名称与 ID 构成一组，状态带文字，地址单独成列。每行最多两个主要管理入口；停止、重启、终止与更换 IP 进入实例上下文操作视图，不能同时排成一整行按钮。数据详情默认折叠。行操作触发器等高并对齐基线；展开的 IP 选项等宽、左对齐，间距使用 `{spacing.sm}`。按钮不通过收缩挤压文字；说明与按钮不足一行时换行。筛选控件和提交按钮底边对齐，标签无论包裹控件或独立放置，都保留一致的 6px 标签间距。
- **Wavelength**：先按父区域显示 Zones 和开通状态；按组选择开通并预览全部受影响 Zones。已开通 Zone 进入实例与网络工作视图；Wavelength 实例同时展示父区域入口 EIP 与出站 Carrier IP，入口 EC2 作为配套资源展示。创建预览明确 2 台 EC2、各自存储与地址费用；更换入站 EIP 和出站 Carrier IP 由实例操作选择。未知配额及旧快照仍显示未知。
- **实例流量规则**：新建 EC2 自动绑定专用的全放行安全组；Lightsail 自动放行全部公网入站，云端出站默认允许。表单不要求手工选择安全组或填写 SSH 来源。预览明确显示适用的 IPv4/IPv6、所有协议和端口，系统防火墙及网络 ACL 独立生效。
- **实例地址**：固定 IP 随实例创建自动分配、绑定；更换确认新绑定后自动释放旧地址。没有独立公网 IP 导航或全局地址编辑页。纯 IPv6 Lightsail 明确不分配 IPv4。终止只自动清理已核对创建记录的配套入口和地址，预览列出具体资源。
- **区域选择卡片**：使用等分网格，同行等宽且卡片等高；复选框放在卡片内固定列，与首行文字对齐，禁用或选中不改变几何尺寸。窄屏改为单列。
- **创建/管理视图**：标题、返回列表、字段分区和底部主操作组成单一任务流；生命周期变更显示选中的实例。root 密码字段不回显，切回密钥方式清空密码。错误后保留非秘密选择并聚焦可理解的错误提示。
- **实例工作区**：类型切换（全部 / EC2 / Lightsail）用分段控件，其他快照视图为次级链接；筛选为单行工具栏。列表行可选中，详情面板分“网络 / 配置 / 归属与同步”，操作按钮只链接到既有预览表单，终止等危险操作收进“更多”。
- **任务中心**：左侧任务列表，右侧任务详情：状态、发起人、时间、成功/失败/进行中进度条，目标与步骤分层展示，失败步骤可勾选后恢复，进度记录按时间排列。
- **账号与代理**：账号行可展开，内联显示访问连接、出口、验证状态与“验证 / 编辑”；管理员在右侧看到代理出口及使用情况。
- **创建摘要**：创建表单右侧固定摘要栏，实时显示实例数、每组账号区域、按目录价的月费估算和自动完成事项；预览按钮与表单提交一致。
- **主按钮**：使用 `{components.primary-button}`，hover 使用 `{components.primary-button-hover}`。焦点使用不透明 3px `{colors.primary}` 外轮廓、2px 偏移；active 不移动布局；disabled 保留原生禁用语义。提交中禁用重复提交并提供可见进行中状态。
- **危险操作**：默认列表不突出“终止/释放”。选定危险操作后再显示红色提示和明确的预览按钮；不会减少既有的预览、权限、云端写入开关或服务端验证。
- **状态与提示**：成功/警告/失败使用对应语义组件。持久的使用说明缩成一行或折叠说明；错误靠近相应操作，不能把技术实现细节变成默认产品流程。
- **空状态**：一个简洁标题、一句说明和与用户权限匹配的下一步。没有查询就显示“选择范围并加载”，不声称账号没有资源。不展示伪造统计数字。
- **仪表盘**：首页标题为“概览”，账号/区域筛选与“刷新账号池”在页头右侧；统计卡片下方是“需要处理”列表（未验证连接、读取不完整、配额 ≥80%、服务端提示，每项带一个处理入口，无事项时显示空状态）与账号池配额。以真实账号池统计为主：账号、EC2、Lightsail、公网 IPv4、已启用账号区域，以及 EC2/Lightsail vCPU 与 Bedrock 配额。统计数使用 `{typography.metric}`，移动端使用 `{typography.metric-mobile}`，标签使用 `{typography.metric-label}`。无数据用“—”，有已确认值但范围不完整时显示部分覆盖，不把未知当成零。
- **配额汇总**：只合并相同类别、模型、单位与周期，账号级全局配额去重；展示已返回额度的数值合计和覆盖范围，不暗示区域额度可互换。Bedrock 没有实时用量时不显示剩余额度。仅在上限和用量均完整、足够新时显示用量条。
- **数据刷新**：后台逐账号、区域查询；界面自动更新已存快照，保留过滤条件。临时刷新失败保留原读数并提示。资源卡片标注最早快照时间，技术运行状态折叠放在页面末尾。
- **账号代理**：添加/编辑账号时直接选择默认 SOCKS5 或 HTTP 代理；新连接默认继承账号出口，独立覆盖作为明确的高级选择。代理认证不回显，非管理员不展示出口配置。
- **运动**：只允许短暂颜色变化，最长 120ms；无弹跳/滑入/自动滚动。prefers-reduced-motion 下关闭过渡和动画。所有主要操作可用 Tab/Enter/Space 完成。

## Do's and Don'ts

### Do

- 先显示资源和范围，再进入一个具体操作。
- 保持信息顺序、按钮位置、边界和间距一致。
- 对未知状态、缓存快照和只读预览作准确说明。
- 使用真实数据、原生表单语义和可见焦点；在桌面与移动宽度验收。
- 修改 UI 时同步检查本文件与共享 CSS 的 token。

### Don't

- 不把所有服务和管理入口平铺在顶部。
- 不在一个页面同时展开列表、创建、网络准备和生命周期表单。
- 不用一排同权重按钮承载所有行操作。
- 不使用低对比文案、大面积彩色卡片、渐变、背景纹理或无意义动画。
- 不更换为参考品牌的标识/专有字体，不改变既有账号权限和云端操作保护。
