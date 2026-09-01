import { defineUserConfig } from 'vuepress'
import { viteBundler } from '@vuepress/bundler-vite'
import { defaultTheme } from '@vuepress/theme-default'
import { searchPlugin } from '@vuepress/plugin-search'
import type { SidebarConfig } from 'vuepress'

const sidebar: SidebarConfig = [
  { text: '文档首页', link: '/' },
  {
    text: '开始使用',
    children: [
      { text: '安装部署', link: '/install.html' },
      { text: '快速开始', link: '/start.html' },
      { text: '运行命令速查', link: '/run.html' },
      { text: '使用示例', link: '/extend/example.html' },
      { text: '隧道模式', link: '/tunnel.html' },
    ],
  },
  {
    text: '服务端',
    children: [
      { text: '服务端介绍', link: '/introduction.html' },
      { text: '服务端使用', link: '/server/nps_use.html' },
      { text: '服务端配置', link: '/server_config.html' },
      { text: '服务端增强功能', link: '/server/nps_extend.html' },
      { text: 'Docker 部署', link: '/docker.html' },
      { text: '宝塔面板部署', link: '/bt.html' },
    ],
  },
  {
    text: '客户端',
    children: [
      { text: '客户端配置', link: '/client_config.html' },
      { text: '配置文件参考', link: '/client/config-file.html' },
      { text: '客户端使用', link: '/client/use.html' },
      { text: '客户端增强功能', link: '/client/npc_extend.html' },
      { text: 'NPC GUI 客户端', link: '/gui.html' },
      { text: 'NPC SDK', link: '/client/npc_sdk.html' },
    ],
  },
  {
    text: '功能与管理',
    children: [
      { text: '功能概览', link: '/extend/feature.html' },
      { text: '运行说明', link: '/extend/description.html' },
      { text: '用户体系', link: '/user.html' },
      { text: '系统架构', link: '/architecture.html' },
      { text: '升级迁移', link: '/migrate.html' },
      { text: '构建发布', link: '/build.html' },
    ],
  },
  {
    text: 'API',
    children: [
      { text: 'Web API 鉴权', link: '/extend/api.html' },
      { text: 'Web API 清单', link: '/extend/webapi.html' },
    ],
  },
  {
    text: '社区',
    children: [
      { text: 'FAQ', link: '/faq.html' },
      { text: '贡献', link: '/contribute.html' },
      { text: '交流', link: '/discuss.html' },
      { text: '捐助', link: '/donate.html' },
      { text: '致谢', link: '/thanks.html' },
      { text: '更新日志', link: '/changelog.html' },
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
    ['link', { rel: 'icon', href: '/nps/logo.svg' }],
    ['meta', { name: 'theme-color', content: '#0f766e' }],
  ],

  plugins: [
    searchPlugin({}),
  ],

  theme: defaultTheme({
    logo: '/logo.svg',
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
        text: '开始使用',
        children: [
          { text: '安装部署', link: '/install.html' },
          { text: '快速开始', link: '/start.html' },
          { text: '隧道模式', link: '/tunnel.html' },
          { text: '使用示例', link: '/extend/example.html' },
        ],
      },
      {
        text: '服务端',
        children: [
          { text: '使用说明', link: '/server/nps_use.html' },
          { text: '配置文件', link: '/server_config.html' },
          { text: '增强功能', link: '/server/nps_extend.html' },
          { text: 'Docker 部署', link: '/docker.html' },
        ],
      },
      {
        text: '客户端',
        children: [
          { text: '配置与启动', link: '/client_config.html' },
          { text: '配置文件参考', link: '/client/config-file.html' },
          { text: '使用说明', link: '/client/use.html' },
          { text: '增强功能', link: '/client/npc_extend.html' },
          { text: 'SDK', link: '/client/npc_sdk.html' },
        ],
      },
      {
        text: '功能与 API',
        children: [
          { text: '功能概览', link: '/extend/feature.html' },
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
          { text: '更新日志', link: '/changelog.html' },
        ],
      },
    ],
    sidebar,
    sidebarDepth: 2,
  }),
})
