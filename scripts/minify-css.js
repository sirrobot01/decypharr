#!/usr/bin/env node

const fs = require('fs');
const path = require('path');
const CleanCSS = require('clean-css');

const sourceDir = './pkg/web/assets/css';
const buildDir = './pkg/web/assets/build/css';

// Create build directory
if (!fs.existsSync(buildDir)) {
    fs.mkdirSync(buildDir, { recursive: true });
}

// Create source directory if it doesn't exist
if (!fs.existsSync(sourceDir)) {
    fs.mkdirSync(sourceDir, { recursive: true });
}

const cleanCSS = new CleanCSS({
    level: 2, // Aggressive optimization
    returnPromise: false
});

function minifyFile(inputPath, outputPath) {
    try {
        console.log(`🎨 Minifying ${path.basename(inputPath)}...`);

        const css = fs.readFileSync(inputPath, 'utf8');
        const result = cleanCSS.minify(css);

        if (result.errors.length > 0) {
            throw new Error(result.errors.join('\n'));
        }

        fs.writeFileSync(outputPath, result.styles);

        // Show size reduction
        const originalSize = Buffer.byteLength(css, 'utf8');
        const minifiedSize = Buffer.byteLength(result.styles, 'utf8');
        const reduction = ((originalSize - minifiedSize) / originalSize * 100).toFixed(1);

        console.log(`   ✓ ${path.basename(inputPath)}: ${(originalSize/1024).toFixed(1)}KB → ${(minifiedSize/1024).toFixed(1)}KB (${reduction}% reduction)`);

        return { original: originalSize, minified: minifiedSize };

    } catch (error) {
        console.error(`   ✗ Error minifying ${inputPath}:`, error.message);
        return null;
    }
}

function minifyAllCSS() {
    console.log('🎨 Minifying additional CSS files...\n');

    try {
        // Get all CSS files from source directory (excluding the main styles.css which is built by Tailwind)
        const cssFiles = fs.readdirSync(sourceDir).filter(file =>
            file.endsWith('.css') && file !== 'styles.css'
        );

        if (cssFiles.length === 0) {
            console.log('ℹ️  No additional CSS files found to minify');
            return;
        }

        let totalOriginal = 0;
        let totalMinified = 0;
        let processedFiles = 0;

        // Minify each file
        cssFiles.forEach(file => {
            const inputPath = path.join(sourceDir, file);
            const outputPath = path.join(buildDir, file);
            const result = minifyFile(inputPath, outputPath);

            if (result) {
                totalOriginal += result.original;
                totalMinified += result.minified;
                processedFiles++;
            }
        });

        if (processedFiles > 0) {
            const totalReduction = ((totalOriginal - totalMinified) / totalOriginal * 100).toFixed(1);
            console.log(`\n✅ Successfully minified ${processedFiles}/${cssFiles.length} additional CSS file(s)`);
            console.log(`📊 Total: ${(totalOriginal/1024).toFixed(1)}KB → ${(totalMinified/1024).toFixed(1)}KB (${totalReduction}% reduction)`);
        }

    } catch (error) {
        console.error('💥 Error during CSS minification:', error);
        process.exit(1);
    }
}

minifyAllCSS();