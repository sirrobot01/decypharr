import {defineConfig} from 'astro/config';
import starlight from '@astrojs/starlight';

const docsBasePath = normalizeBasePath(process.env.PUBLIC_DOCS_BASE_PATH || '/');

// https://astro.build/config
export default defineConfig({
    base: docsBasePath,
    redirects: {
        '/guides/mounting/rclone': '/guides/mounting/rclone-internal',
        '/guides/mounting/webdav': '/guides/shares/webdav',
        '/guides/mounting/nfs': '/guides/shares/nfs',
        '/guides/mounting/smb': '/guides/shares/smb',
    },
    integrations: [
        starlight({
            title: 'Decypharr',
            favicon: '/favicon.png',
            logo: {
                src: './src/assets/logo.png',
            },
            components: {
                Header: './src/components/Header.astro',
            },
            social: [
                {label: 'GitHub', icon: 'github', href: 'https://github.com/sirrobot01/decypharr'},
            ],
            sidebar: [
                {
                    label: 'Getting Started',
                    items: [
                        {label: 'Installation', link: '/guides/installation'},
                        {label: 'Setup Wizard', link: '/guides/quick-start'},
                        {label: 'Features', link: '/features'},
                    ],
                },
                {
                    label: 'Configuration',
                    items: [
                        {label: 'Configuration Reference', link: '/guides/configuration'},
                        {label: 'Virtual Folders', link: '/guides/virtual-folders'},
                    ],
                },
                {
                    label: 'Debrid Providers',
                    items: [
                        {label: 'Real Debrid', link: '/guides/debrid/real-debrid'},
                        {label: 'All Debrid', link: '/guides/debrid/all-debrid'},
                        {label: 'Debrid Link', link: '/guides/debrid/debrid-link'},
                        {label: 'Torbox', link: '/guides/debrid/torbox'},
                    ],
                },
                {
                    label: 'Usenet',
                    items: [
                        {label: 'Overview', link: '/guides/usenet/overview'},
                        {label: 'Sabnzbd API', link: '/guides/usenet/sabnzbd'},
                    ],
                },
                {
                    label: 'Mounting',
                    items: [
                        {label: 'DFS (Custom VFS)', link: '/guides/mounting/dfs'},
                        {label: 'Rclone (Internal)', link: '/guides/mounting/rclone-internal'},
                        {label: 'Rclone (External)', link: '/guides/mounting/rclone-external'},
                    ],
                },
                {
                    label: 'Shares',
                    items: [
                        {label: 'Overview', link: '/guides/shares/overview'},
                        {label: 'WebDAV Server', link: '/guides/shares/webdav'},
                        {label: 'NFS Server', link: '/guides/shares/nfs'},
                        {label: 'SMB Server', link: '/guides/shares/smb'},
                    ],
                },
                {
                    label: 'Integration',
                    items: [
                        {label: 'Sonarr & Radarr', link: '/guides/arrs'},
                        {label: 'Repair Worker', link: '/guides/repair'},
                    ],
                },
                {
                    label: 'Reference',
                    items: [
                        {label: 'API', link: '/reference/api'},
                    ],
                },
                {
                    label: 'Help',
                    items: [
                        {label: 'FAQ', link: '/help/faq'},
                        {label: 'Troubleshooting', link: '/help/troubleshooting'},
                    ],
                },
            ],
        }),
    ],
});

function normalizeBasePath(value) {
    if (!value) {
        return '/';
    }

    let normalized = value.trim();

    if (!normalized.startsWith('/')) {
        normalized = `/${normalized}`;
    }

    if (!normalized.endsWith('/')) {
        normalized = `${normalized}/`;
    }

    return normalized.replace(/\/{2,}/g, '/');
}
