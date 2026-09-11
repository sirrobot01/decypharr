#!/usr/bin/env node

import { createWriteStream, existsSync, mkdirSync, statSync, writeFileSync } from 'fs';
import { get } from 'https';
import { basename, join } from 'path';

const buildDir = {
    css: '../frontend/assets/build/css',
    js: '../frontend/assets/build/js',
    fonts: '../frontend/assets/build/fonts'
};

// Create directories
Object.values(buildDir).forEach(dir => {
    if (!existsSync(dir)) {
        mkdirSync(dir, {recursive: true});
    }
});

// Download function
function downloadFile(url, filepath) {
    return new Promise((resolve, reject) => {
        console.log(`📥 Downloading ${basename(filepath)}...`);

        const file = createWriteStream(filepath);

        get(url, (response) => {
            if (response.statusCode === 200) {
                response.pipe(file);
                file.on('finish', () => {
                    file.close();
                    const stats = statSync(filepath);
                    const size = (stats.size / 1024).toFixed(1) + 'KB';
                    console.log(`   ✓ Downloaded ${basename(filepath)} (${size})`);
                    resolve();
                });
            } else if (response.statusCode === 302 || response.statusCode === 301) {
                downloadFile(response.headers.location, filepath).then(resolve).catch(reject);
            } else {
                reject(new Error(`Failed to download ${url}: ${response.statusCode}`));
            }
        }).on('error', reject);
    });
}

// Download text content
function downloadText(url) {
    return new Promise((resolve, reject) => {
        get(url, (response) => {
            let data = '';
            response.on('data', chunk => data += chunk);
            response.on('end', () => {
                if (response.statusCode === 200) {
                    resolve(data);
                } else {
                    reject(new Error(`Failed to download ${url}: ${response.statusCode}`));
                }
            });
        }).on('error', reject);
    });
}

// Files to download
const downloads = [
    {
        url: 'https://cdn.jsdelivr.net/npm/bootstrap-icons@1.11.2/font/fonts/bootstrap-icons.woff',
        path: join(buildDir.fonts, 'bootstrap-icons.woff')
    },
    {
        url: 'https://cdn.jsdelivr.net/npm/bootstrap-icons@1.11.2/font/fonts/bootstrap-icons.woff2',
        path: join(buildDir.fonts, 'bootstrap-icons.woff2')
    },
    {
        url: 'https://code.jquery.com/jquery-3.7.1.min.js',
        path: join(buildDir.js, 'jquery-3.7.1.min.js')
    }
];

// Download all files
async function downloadAssets() {
    console.log('📦 Downloading external assets...\n');

    try {
        // Download Bootstrap Icons CSS and fix paths
        console.log('📥 Downloading Bootstrap Icons CSS...');
        const biCSS = await downloadText('https://cdn.jsdelivr.net/npm/bootstrap-icons@1.11.2/font/bootstrap-icons.css');

        // Fix font paths to point to our local fonts
        const fixedCSS = biCSS.replace(
            /url\("\.\/fonts\//g,
            'url("../fonts/'
        );

        // Write fixed CSS to source directory so it can be minified
        const biCSSSourcePath = join('../frontend/assets/css', 'bootstrap-icons.css');
        writeFileSync(biCSSSourcePath, fixedCSS);
        console.log(`   ✓ Downloaded Bootstrap Icons CSS (${(fixedCSS.length / 1024).toFixed(1)}KB)`);

        // Download other assets
        for (const download of downloads) {
            await downloadFile(download.url, download.path);
        }

        console.log('\nExternal assets downloaded successfully!');

    } catch (error) {
        console.error('💥 Error downloading assets:', error);
        process.exit(1);
    }
}

downloadAssets();