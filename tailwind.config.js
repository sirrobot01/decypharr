module.exports = {
    content: [
        "./pkg/web/templates/**/*.html",
        "./pkg/web/assets/**/*.js"
    ],
    theme: {
        extend: {},
    },
    plugins: [
        require('daisyui'),
    ],
};