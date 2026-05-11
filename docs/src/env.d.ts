/// <reference types="astro/client" />

interface ImportMetaEnv {
    readonly PUBLIC_DOCS_BASE_PATH?: string;
}

interface ImportMeta {
    readonly env: ImportMetaEnv;
}
