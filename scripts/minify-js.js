#!/usr/bin/env node

const fs = require('fs');
const path = require('path');
const { minify } = require('terser');

const sourceDir = './pkg/web/assets/js';
const buildDir = './pkg/web/assets/build/js';

// Create build directory
if (!fs.existsSync(buildDir)) {
    fs.mkdirSync(buildDir, { recursive: true });
}

// Minify options
const minifyOptions = {
    compress: {
        drop_console: false, // Keep console.log for debugging
        drop_debugger: true,
        dead_code: true,
        unused: true,
        sequences: true,
        conditionals: true,
        booleans: true,
        if_return: true,
        join_vars: true,
    },
    mangle: {
        toplevel: false,
        reserved: [
            '$', 'jQuery', 'decypharrUtils', 'configManager', 'repairManager',
            'RepairManager', 'RepairUtils', 'ConfigManager', 'window', 'document'
        ]
    },
    format: {
        comments: false,
        beautify: false
    }
};

async function minifyFile(inputPath, outputPath) {
    try {
        console.log(`🗜️  Minifying ${path.basename(inputPath)}...`);

        const code = fs.readFileSync(inputPath, 'utf8');
        const result = await minify(code, minifyOptions);

        if (result.error) {
            throw result.error;
        }

        fs.writeFileSync(outputPath, result.code);

        // Show size reduction
        const originalSize = fs.statSync(inputPath).size;
        const minifiedSize = fs.statSync(outputPath).size;
        const reduction = ((originalSize - minifiedSize) / originalSize * 100).toFixed(1);

        console.log(`   ✓ ${path.basename(inputPath)}: ${(originalSize/1024).toFixed(1)}KB → ${(minifiedSize/1024).toFixed(1)}KB (${reduction}% reduction)`);

        return { original: originalSize, minified: minifiedSize };

    } catch (error) {
        console.error(`   ✗ Error minifying ${inputPath}:`, error.message);
        return null;
    }
}

async function minifyAllJS() {
    console.log('📦 Minifying JavaScript files...\n');

    try {
        // Check if source directory exists
        if (!fs.existsSync(sourceDir)) {
            console.log(`Creating source directory ${sourceDir}...`);
            fs.mkdirSync(sourceDir, { recursive: true });
            console.log('ℹ️  No JavaScript files found to minify');
            return;
        }

        // Get all JS files from source directory
        const jsFiles = fs.readdirSync(sourceDir).filter(file => file.endsWith('.js'));

        if (jsFiles.length === 0) {
            console.log('ℹ️  No JavaScript files found to minify');
            return;
        }

        let totalOriginal = 0;
        let totalMinified = 0;
        let processedFiles = 0;

        // Minify each file
        for (const file of jsFiles) {
            const inputPath = path.join(sourceDir, file);
            const outputPath = path.join(buildDir, file);
            const result = await minifyFile(inputPath, outputPath);

            if (result) {
                totalOriginal += result.original;
                totalMinified += result.minified;
                processedFiles++;
            }
        }

        if (processedFiles > 0) {
            const totalReduction = ((totalOriginal - totalMinified) / totalOriginal * 100).toFixed(1);
            console.log(`\n✅ Successfully minified ${processedFiles}/${jsFiles.length} JavaScript file(s)`);
            console.log(`📊 Total: ${(totalOriginal/1024).toFixed(1)}KB → ${(totalMinified/1024).toFixed(1)}KB (${totalReduction}% reduction)`);
        }

    } catch (error) {
        console.error('💥 Error during JavaScript minification:', error);
        process.exit(1);
    }
}

minifyAllJS();