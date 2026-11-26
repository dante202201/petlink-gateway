/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_DEV_PORT?: string;
  readonly VITE_GATEWAY_ORIGIN?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}

declare module "*.md?raw" {
  const content: string;
  export default content;
}

declare module "@repo-docs/*.md?raw" {
  const content: string;
  export default content;
}
