/// <reference types="vite/client" />

declare module '*.vue' {
  import type { DefineComponent } from 'vue'
  const component: DefineComponent<{}, {}, any>
  export default component
}

declare module 'markdown-it' {
  interface Options {
    html?: boolean
    xhtmlOut?: boolean
    breaks?: boolean
    langPrefix?: string
    linkify?: boolean
    typographer?: boolean
    quotes?: string | string[]
    highlight?: (str: string, lang: string) => string
  }

  export default class MarkdownIt {
    constructor(options?: Options)
    render(src: string, env?: any): string
    utils: {
      escapeHtml(str: string): string
    }
  }
}

declare module 'node:url' {
  export function fileURLToPath(url: URL | string): string
}
