import { defineUserConfig } from 'vuepress'
import { viteBundler } from '@vuepress/bundler-vite'
import { defaultTheme } from '@vuepress/theme-default'
import { searchPlugin } from '@vuepress/plugin-search'
import type { SidebarConfig } from 'vuepress'

const sidebar: SidebarConfig = [
  { text: '文档首页', link: '/' },
  {
    text: '快速上手',
    children: [
      { text: '安装与部署', link: '/install/' },
      { text: '快速开始', link: '/start.html' },
      { text: '运行命令速查', link: '/run.html' },
      { text: '完整部署参考', link: '/install.html' },
      { text: '隧道模式', link: '/tunnel.html' },
      { text: '使用示例', link: '/extend/example.html' },
    ],
  },
  {
    text: '服务端',
    children: [
      { text: '服务端使用', link: '/server/nps_use.html' },
      { text: '配置文件参考', link: '/server/server_config.html' },
      { text: '服务端增强功能', link: '/server/nps_extend.html' },
      { text: '服务端介绍', link: '/introduction.html' },
      { text: '部署安全与参数速查', link: '/server_config.html' },
      { text: 'Docker 部署', link: '/docker.html' },
      { text: '宝塔面板部署', link: '/bt.html' },
    ],
  },
  {
    text: '客户端',
    children: [
      { text: '客户端使用', link: '/client/use.html' },
      { text: '客户端增强功能', link: '/client/npc_extend.html' },
      { text: 'NPC SDK', link: '/client/npc_sdk.html' },
      { text: '客户端配置与启动', link: '/client_config.html' },
      { text: '配置文件参考', link: '/client/config-file.html' },
      { text: 'NPC GUI 客户端', link: '/gui.html' },
    ],
  },
  {
    text: '扩展功能',
    children: [
      { text: '功能概览', link: '/extend/feature.html' },
      { text: '域名代理与路由', link: '/extend/domain-proxy.html' },
      { text: '平台域名与证书诊断', link: '/extend/platform-domain.html' },
      { text: '访问控制与配额', link: '/extend/access-control.html' },
      { text: '运行说明', link: '/extend/description.html' },
      { text: 'Web API 鉴权', link: '/extend/api.html' },
      { text: 'Web API 清单', link: '/extend/webapi.html' },
      { text: '使用示例', link: '/extend/example.html' },
    ],
  },
  {
    text: '项目与社区',
    children: [
      { text: '用户体系', link: '/user.html' },
      { text: '系统架构', link: '/architecture.html' },
      { text: '升级迁移', link: '/migrate.html' },
      { text: '构建发布', link: '/build.html' },
      { text: '本项目更新日志', link: '/changelog.html' },
      { text: '上游更新日志', link: '/changelog/' },
      { text: 'FAQ', link: '/faq.html' },
      { text: '贡献', link: '/contribute.html' },
      { text: '交流', link: '/discuss.html' },
      { text: '捐助', link: '/donate.html' },
      { text: '致谢', link: '/thanks.html' },
    ],
  },
]

export default defineUserConfig({
  base: '/nps/',
  bundler: viteBundler(),
  lang: 'zh-CN',
  title: 'NPS',
  description: 'NPS 内网穿透文档：安装、服务端、客户端、隧道、功能、运维和 Web API。',
  head: [
    ['link', { rel: 'icon', href: '/nps/logo.png' }],
    ['meta', { name: 'theme-color', content: '#0f766e' }],
  ],

  plugins: [
    searchPlugin({}),
  ],

  theme: defaultTheme({
    logo: '/logo.png',
    repo: 'ZiDuNet/nps',
    repoLabel: 'GitHub',
    docsRepo: 'https://github.com/ZiDuNet/nps',
    docsBranch: 'master',
    docsDir: 'docs',
    editLink: true,
    editLinkText: '在 GitHub 上编辑此页',
    lastUpdated: true,
    lastUpdatedText: '最后更新',
    contributors: false,

    navbar: [
      { text: '首页', link: '/' },
      {
        text: '快速上手',
        children: [
          { text: '安装与部署', link: '/install/' },
          { text: '快速开始', link: '/start.html' },
          { text: '运行命令速查', link: '/run.html' },
          { text: '隧道模式', link: '/tunnel.html' },
          { text: '使用示例', link: '/extend/example.html' },
        ],
      },
      {
        text: '服务端',
        children: [
          { text: '使用说明', link: '/server/nps_use.html' },
          { text: '配置文件', link: '/server/server_config.html' },
          { text: '增强功能', link: '/server/nps_extend.html' },
          { text: 'Docker 部署', link: '/docker.html' },
          { text: '参数与安全速查', link: '/server_config.html' },
        ],
      },
      {
        text: '客户端',
        children: [
          { text: '使用说明', link: '/client/use.html' },
          { text: '增强功能', link: '/client/npc_extend.html' },
          { text: '配置与启动', link: '/client_config.html' },
          { text: '配置文件参考', link: '/client/config-file.html' },
          { text: 'SDK', link: '/client/npc_sdk.html' },
        ],
      },
      {
        text: '扩展功能',
        children: [
          { text: '功能概览', link: '/extend/feature.html' },
          { text: '域名代理与路由', link: '/extend/domain-proxy.html' },
          { text: '平台域名与证书诊断', link: '/extend/platform-domain.html' },
          { text: '访问控制与配额', link: '/extend/access-control.html' },
          { text: '运行说明', link: '/extend/description.html' },
          { text: '配置示例', link: '/extend/example.html' },
          { text: 'API 鉴权', link: '/extend/api.html' },
          { text: 'API 清单', link: '/extend/webapi.html' },
        ],
      },
      {
        text: '更多',
        children: [
          { text: '用户体系', link: '/user.html' },
          { text: 'FAQ', link: '/faq.html' },
          { text: '升级迁移', link: '/migrate.html' },
          { text: '本项目更新日志', link: '/changelog.html' },
          { text: '上游更新日志', link: '/changelog/' },
        ],
      },
    ],
    sidebar,
    sidebarDepth: 3,
  }),
})
