// Common utilities and functions
class DecypharrUtils {
    constructor() {
        this.urlBase = window.urlBase || '';
        this.toastContainer = null;
        this.init();
    }

    init() {
        this.setupToastSystem();
        this.setupThemeToggle();
        this.setupPasswordToggles();
        this.setupVersionInfo();
        this.setupGlobalEventListeners();
        this.createToastContainer();
    }

    // Create toast container if it doesn't exist
    createToastContainer() {
        let container = document.querySelector('.toast-container');
        if (!container) {
            container = document.createElement('div');
            container.className = 'toast-container fixed bottom-4 right-4 z-50 space-y-2';
            document.body.appendChild(container);
        }
        this.toastContainer = container;
    }

    // Setup toast system
    setupToastSystem() {
        // Add toast CSS styles
        this.addToastStyles();

        // Global toast handler
        window.addEventListener('error', (e) => {
            console.error('Global error:', e.error);
            this.createToast(`Unexpected error: ${e.error?.message || 'Unknown error'}`, 'error');
        });

        // Handle unhandled promise rejections
        window.addEventListener('unhandledrejection', (e) => {
            console.error('Unhandled promise rejection:', e.reason);
            this.createToast(`Promise rejected: ${e.reason?.message || 'Unknown error'}`, 'error');
        });
    }

    // Add toast styles to document
    addToastStyles() {
        if (document.getElementById('toast-styles')) return;

        const style = document.createElement('style');
        style.id = 'toast-styles';
        style.textContent = `
            @keyframes toastSlideIn {
                from {
                    opacity: 0;
                    transform: translateX(100%);
                }
                to {
                    opacity: 1;
                    transform: translateX(0);
                }
            }

            @keyframes toastSlideOut {
                from {
                    opacity: 1;
                    transform: translateX(0);
                }
                to {
                    opacity: 0;
                    transform: translateX(100%);
                }
            }

            .toast-container .alert {
                animation: toastSlideIn 0.3s ease-out;
                max-width: 400px;
                word-wrap: break-word;
            }

            .toast-container .alert.toast-closing {
                animation: toastSlideOut 0.3s ease-in forwards;
            }

            @media (max-width: 640px) {
                .toast-container {
                    left: 1rem;
                    right: 1rem;
                    bottom: 1rem;
                }
                
                .toast-container .alert {
                    max-width: none;
                }
            }
        `;
        document.head.appendChild(style);
    }

    // URL joining utility
    joinURL(base, path) {
        if (!base.endsWith('/')) base += '/';
        if (path.startsWith('/')) path = path.substring(1);
        return base + path;
    }

    // Enhanced fetch wrapper
    async fetcher(endpoint, options = {}) {
        const url = this.joinURL(this.urlBase, endpoint);

        // Handle FormData - don't set Content-Type for FormData
        const defaultOptions = {
            headers: {},
            ...options
        };

        // Only set Content-Type if not FormData
        if (!(options.body instanceof FormData)) {
            defaultOptions.headers['Content-Type'] = 'application/json';
        }

        // Merge headers
        defaultOptions.headers = {
            ...defaultOptions.headers,
            ...options.headers
        };

        try {
            const response = await fetch(url, defaultOptions);

            // Add loading state management
            if (options.loadingButton) {
                this.setButtonLoading(options.loadingButton, false);
            }

            return response;
        } catch (error) {
            if (options.loadingButton) {
                this.setButtonLoading(options.loadingButton, false);
            }
            throw error;
        }
    }

    // Enhanced toast system
    createToast(message, type = 'success', duration = null) {
        const toastTimeouts = {
            success: 5000,
            warning: 10000,
            error: 15000,
            info: 7000
        };

        type = ['success', 'warning', 'error', 'info'].includes(type) ? type : 'success';
        duration = duration || toastTimeouts[type];

        // Ensure toast container exists
        if (!this.toastContainer) {
            this.createToastContainer();
        }

        const toastId = `toast-${Date.now()}-${Math.random().toString(36).substr(2, 9)}`;

        const alertTypeClass = {
            success: 'alert-success',
            warning: 'alert-warning',
            error: 'alert-error',
            info: 'alert-info'
        };

        const icons = {
            success: '<path fill-rule="evenodd" d="M10 18a8 8 0 100-16 8 8 0 000 16zm3.707-9.293a1 1 0 00-1.414-1.414L9 10.586 7.707 9.293a1 1 0 00-1.414 1.414l2 2a1 1 0 001.414 0l4-4z" clip-rule="evenodd"></path>',
            error: '<path fill-rule="evenodd" d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-7 4a1 1 0 11-2 0 1 1 0 012 0zm-1-9a1 1 0 00-1 1v4a1 1 0 102 0V6a1 1 0 00-1-1z" clip-rule="evenodd"></path>',
            warning: '<path fill-rule="evenodd" d="M8.257 3.099c.765-1.36 2.722-1.36 3.486 0l5.58 9.92c.75 1.334-.213 2.98-1.742 2.98H4.42c-1.53 0-2.493-1.646-1.743-2.98l5.58-9.92zM11 13a1 1 0 11-2 0 1 1 0 012 0zm-1-8a1 1 0 00-1 1v3a1 1 0 002 0V6a1 1 0 00-1-1z" clip-rule="evenodd"></path>',
            info: '<path fill-rule="evenodd" d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-7-4a1 1 0 11-2 0 1 1 0 012 0zM9 9a1 1 0 000 2v3a1 1 0 001 1h1a1 1 0 100-2v-3a1 1 0 00-1-1H9z" clip-rule="evenodd"></path>'
        };

        const toastHtml = `
            <div id="${toastId}" class="alert ${alertTypeClass[type]} shadow-lg mb-2">
                <div class="flex items-start gap-3">
                    <svg class="w-6 h-6 shrink-0" fill="currentColor" viewBox="0 0 20 20">
                        ${icons[type]}
                    </svg>
                    <div class="flex-1">
                        <span class="text-sm">${message.replace(/\n/g, '<br>')}</span>
                    </div>
                    <button class="btn btn-sm btn-ghost btn-circle" onclick="window.decypharrUtils.closeToast('${toastId}');">
                        <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"></path>
                        </svg>
                    </button>
                </div>
            </div>
        `;

        this.toastContainer.insertAdjacentHTML('beforeend', toastHtml);

        // Auto-close toast
        const timeoutId = setTimeout(() => this.closeToast(toastId), duration);

        // Store timeout ID for manual closing
        const toastElement = document.getElementById(toastId);
        if (toastElement) {
            toastElement.dataset.timeoutId = timeoutId;
        }

        return toastId;
    }

    closeToast(toastId) {
        const toastElement = document.getElementById(toastId);
        if (toastElement) {
            // Clear auto-close timeout
            if (toastElement.dataset.timeoutId) {
                clearTimeout(parseInt(toastElement.dataset.timeoutId));
            }

            toastElement.classList.add('toast-closing');
            setTimeout(() => {
                if (toastElement.parentNode) {
                    toastElement.remove();
                }
            }, 300);
        }
    }

    // Close all toasts
    closeAllToasts() {
        const toasts = this.toastContainer?.querySelectorAll('.alert');
        if (toasts) {
            toasts.forEach(toast => {
                if (toast.id) {
                    this.closeToast(toast.id);
                }
            });
        }
    }

    // Button loading state management
    setButtonLoading(buttonElement, loading = true, originalText = null) {
        if (typeof buttonElement === 'string') {
            buttonElement = document.getElementById(buttonElement) || document.querySelector(buttonElement);
        }

        if (!buttonElement) return;

        if (loading) {
            buttonElement.disabled = true;
            if (!buttonElement.dataset.originalText) {
                buttonElement.dataset.originalText = originalText || buttonElement.innerHTML;
            }
            buttonElement.innerHTML = '<span class="loading loading-spinner loading-sm"></span>Processing...';
            buttonElement.classList.add('loading-state');
        } else {
            buttonElement.disabled = false;
            buttonElement.innerHTML = buttonElement.dataset.originalText || 'Submit';
            buttonElement.classList.remove('loading-state');
            delete buttonElement.dataset.originalText;
        }
    }

    // Password field utilities
    setupPasswordToggles() {
        document.addEventListener('click', (e) => {
            const toggleBtn = e.target.closest('.password-toggle-btn');
            if (toggleBtn) {
                e.preventDefault();
                e.stopPropagation();

                // Find the associated input field
                const container = toggleBtn.closest('.password-toggle-container');
                if (container) {
                    const input = container.querySelector('input, textarea');
                    const icon = toggleBtn.querySelector('i');
                    if (input && icon) {
                        this.togglePasswordField(input, icon);
                    }
                }
            }
        });
    }

    togglePasswordField(field, icon) {
        if (!icon) return;

        if (field.tagName.toLowerCase() === 'textarea') {
            this.togglePasswordTextarea(field, icon);
        } else {
            this.togglePasswordInput(field, icon);
        }
    }

    togglePasswordInput(field, icon) {
        if (field.type === 'password') {
            field.type = 'text';
            icon.className = 'bi bi-eye-slash';
        } else {
            field.type = 'password';
            icon.className = 'bi bi-eye';
        }
    }

    togglePasswordTextarea(field, icon) {
        const isHidden = field.style.webkitTextSecurity === 'disc' ||
            field.style.webkitTextSecurity === '' ||
            field.getAttribute('data-password-visible') !== 'true';

        if (isHidden) {
            field.style.webkitTextSecurity = 'none';
            field.style.textSecurity = 'none';
            field.setAttribute('data-password-visible', 'true');
            icon.className = 'bi bi-eye-slash';
        } else {
            field.style.webkitTextSecurity = 'disc';
            field.style.textSecurity = 'disc';
            field.setAttribute('data-password-visible', 'false');
            icon.className = 'bi bi-eye';
        }
    }

    // Legacy methods for backward compatibility
    togglePassword(fieldId) {
        const field = document.getElementById(fieldId);
        const button = field?.closest('.password-toggle-container')?.querySelector('.password-toggle-btn');
        let icon = button.querySelector("i");
        if (field && icon) {
            this.togglePasswordField(field, icon);
        }
    }

    // Theme management
    setupThemeToggle() {
        const themeToggle = document.getElementById('themeToggle');
        const htmlElement = document.documentElement;

        if (!themeToggle) return;

        const setTheme = (theme) => {
            htmlElement.setAttribute('data-theme', theme);
            localStorage.setItem('theme', theme);
            themeToggle.checked = theme === 'dark';

            // Smooth theme transition
            document.body.style.transition = 'background-color 0.3s ease, color 0.3s ease';
            setTimeout(() => {
                document.body.style.transition = '';
            }, 300);

            // Emit theme change event
            window.dispatchEvent(new CustomEvent('themechange', { detail: { theme } }));
        };

        // Load saved theme
        const savedTheme = localStorage.getItem('theme');
        if (savedTheme) {
            setTheme(savedTheme);
        } else if (window.matchMedia?.('(prefers-color-scheme: dark)').matches) {
            setTheme('dark');
        } else {
            setTheme('light');
        }

        // Theme toggle event
        themeToggle.addEventListener('change', () => {
            const currentTheme = htmlElement.getAttribute('data-theme');
            setTheme(currentTheme === 'dark' ? 'light' : 'dark');
        });

        // Listen for system theme changes
        if (window.matchMedia) {
            window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', e => {
                if (!localStorage.getItem('theme')) {
                    setTheme(e.matches ? 'dark' : 'light');
                }
            });
        }
    }

    // Version info
    async setupVersionInfo() {
        try {
            const response = await this.fetcher('/version');
            if (!response.ok) throw new Error('Failed to fetch version');

            const data = await response.json();
            const versionBadge = document.getElementById('version-badge');

            if (versionBadge) {
                versionBadge.innerHTML = `
                    <a href="https://github.com/sirrobot01/decypharr/releases/tag/${data.version}" 
                       target="_blank" 
                       class="text-current hover:text-primary transition-colors">
                        ${data.channel}-${data.version}
                    </a>
                `;

                // Remove existing badge classes
                versionBadge.classList.remove('badge-warning', 'badge-error', 'badge-ghost');

                if (data.channel === 'beta') {
                    versionBadge.classList.add('badge-warning');
                } else if (data.channel === 'nightly') {
                    versionBadge.classList.add('badge-error');
                }
            }
        } catch (error) {
            console.error('Error fetching version:', error);
            const versionBadge = document.getElementById('version-badge');
            if (versionBadge) {
                versionBadge.textContent = 'Unknown';
                versionBadge.classList.add('badge-ghost');
            }
        }
    }

    // Mobile navigation dropdown handler
    setupMobileNavigation() {
        const mobileMenuBtn = document.querySelector('.navbar-start .dropdown [role="button"]');
        const mobileMenu = document.querySelector('.navbar-start .dropdown .dropdown-content');
        const dropdown = document.querySelector('.navbar-start .dropdown');

        if (!mobileMenuBtn || !mobileMenu || !dropdown) return;

        let isOpen = false;

        const openDropdown = () => {
            if (!isOpen) {
                dropdown.classList.add('dropdown-open');
                mobileMenuBtn.setAttribute('aria-expanded', 'true');
                isOpen = true;
            }
        };

        const closeDropdown = () => {
            if (isOpen) {
                dropdown.classList.remove('dropdown-open');
                mobileMenuBtn.setAttribute('aria-expanded', 'false');
                isOpen = false;
            }
        };

        const toggleDropdown = (e) => {
            e.preventDefault();
            e.stopPropagation();
            
            if (isOpen) {
                closeDropdown();
            } else {
                openDropdown();
            }
        };

        // Handle button clicks (both mouse and touch)
        mobileMenuBtn.addEventListener('click', toggleDropdown);
        mobileMenuBtn.addEventListener('touchend', (e) => {
            e.preventDefault();
            toggleDropdown(e);
        });

        // Close dropdown when clicking outside
        document.addEventListener('click', (e) => {
            if (isOpen && !dropdown.contains(e.target)) {
                closeDropdown();
            }
        });

        // Close dropdown when touching outside
        document.addEventListener('touchend', (e) => {
            if (isOpen && !dropdown.contains(e.target)) {
                closeDropdown();
            }
        });

        // Close dropdown when clicking menu items
        mobileMenu.addEventListener('click', (e) => {
            if (e.target.tagName === 'A') {
                closeDropdown();
            }
        });

        // Handle keyboard navigation
        mobileMenuBtn.addEventListener('keydown', (e) => {
            if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault();
                toggleDropdown(e);
            } else if (e.key === 'Escape') {
                closeDropdown();
            }
        });

        // Set initial aria attributes
        mobileMenuBtn.setAttribute('aria-expanded', 'false');
        mobileMenuBtn.setAttribute('aria-haspopup', 'true');
    }

    // Global event listeners
    setupGlobalEventListeners() {
        // Setup mobile navigation dropdown
        this.setupMobileNavigation();

        // Smooth scroll for anchor links
        document.addEventListener('click', (e) => {
            const link = e.target.closest('a[href^="#"]');
            if (link && link.getAttribute('href') !== '#') {
                e.preventDefault();
                const target = document.querySelector(link.getAttribute('href'));
                if (target) {
                    target.scrollIntoView({ behavior: 'smooth', block: 'start' });
                }
            }
        });

        // Enhanced form validation
        document.addEventListener('invalid', (e) => {
            e.target.classList.add('input-error');
            setTimeout(() => e.target.classList.remove('input-error'), 3000);
        }, true);

        // Keyboard shortcuts
        document.addEventListener('keydown', (e) => {
            // Escape key closes modals and dropdowns
            if (e.key === 'Escape') {
                // Close modals
                document.querySelectorAll('.modal[open]').forEach(modal => modal.close());

                // Close dropdowns
                document.querySelectorAll('.dropdown-open').forEach(dropdown => {
                    dropdown.classList.remove('dropdown-open');
                });

                // Close context menus
                document.querySelectorAll('.context-menu:not(.hidden)').forEach(menu => {
                    menu.classList.add('hidden');
                });
            }

            // Ctrl/Cmd + / for help (if help system exists)
            if ((e.ctrlKey || e.metaKey) && e.key === '/') {
                e.preventDefault();
                this.showKeyboardShortcuts();
            }
        });

        // Handle page visibility changes
        document.addEventListener('visibilitychange', () => {
            if (document.hidden) {
                // Page is hidden - pause auto-refresh timers if any
                window.dispatchEvent(new CustomEvent('pageHidden'));
            } else {
                // Page is visible - resume auto-refresh timers if any
                window.dispatchEvent(new CustomEvent('pageVisible'));
            }
        });

        // Handle online/offline status
        window.addEventListener('online', () => {
            this.createToast('Connection restored', 'success');
        });

        window.addEventListener('offline', () => {
            this.createToast('Connection lost - working offline', 'warning');
        });
    }

    // Show keyboard shortcuts modal
    showKeyboardShortcuts() {
        const shortcuts = [
            { key: 'Esc', description: 'Close modals and dropdowns' },
            { key: 'Ctrl + /', description: 'Show this help' },
            { key: 'Ctrl + R', description: 'Refresh page' }
        ];

        const modal = document.createElement('dialog');
        modal.className = 'modal';
        modal.innerHTML = `
            <div class="modal-box">
                <form method="dialog">
                    <button class="btn btn-sm btn-circle btn-ghost absolute right-2 top-2">✕</button>
                </form>
                <h3 class="font-bold text-lg mb-4">Keyboard Shortcuts</h3>
                <div class="space-y-2">
                    ${shortcuts.map(shortcut => `
                        <div class="flex justify-between items-center">
                            <span class="kbd kbd-sm">${shortcut.key}</span>
                            <span class="text-sm">${shortcut.description}</span>
                        </div>
                    `).join('')}
                </div>
            </div>
        `;

        document.body.appendChild(modal);
        modal.showModal();

        modal.addEventListener('close', () => {
            document.body.removeChild(modal);
        });
    }

    // Utility methods
    formatBytes(bytes) {
        if (!bytes || bytes === 0) return '0 B';
        const k = 1024;
        const sizes = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
        const i = Math.floor(Math.log(bytes) / Math.log(k));
        return `${parseFloat((bytes / Math.pow(k, i)).toFixed(2))} ${sizes[i]}`;
    }

    formatSpeed(speed) {
        return `${this.formatBytes(speed)}/s`;
    }

    formatDuration(seconds) {
        if (!seconds || seconds === 0) return '0s';

        const units = [
            { label: 'd', seconds: 86400 },
            { label: 'h', seconds: 3600 },
            { label: 'm', seconds: 60 },
            { label: 's', seconds: 1 }
        ];

        const parts = [];
        let remaining = seconds;

        for (const unit of units) {
            const count = Math.floor(remaining / unit.seconds);
            if (count > 0) {
                parts.push(`${count}${unit.label}`);
                remaining %= unit.seconds;
            }
        }

        return parts.slice(0, 2).join(' ') || '0s';
    }

    // Debounce function
    debounce(func, wait, immediate = false) {
        let timeout;
        return function executedFunction(...args) {
            const later = () => {
                timeout = null;
                if (!immediate) func(...args);
            };
            const callNow = immediate && !timeout;
            clearTimeout(timeout);
            timeout = setTimeout(later, wait);
            if (callNow) func(...args);
        };
    }

    // Throttle function
    throttle(func, limit) {
        let inThrottle;
        return function(...args) {
            if (!inThrottle) {
                func.apply(this, args);
                inThrottle = true;
                setTimeout(() => inThrottle = false, limit);
            }
        };
    }

    // Copy to clipboard utility
    async copyToClipboard(text) {
        try {
            await navigator.clipboard.writeText(text);
            this.createToast('Copied to clipboard', 'success');
            return true;
        } catch (error) {
            console.error('Failed to copy to clipboard:', error);
            this.createToast('Failed to copy to clipboard', 'error');
            return false;
        }
    }

    // Validate URL
    isValidUrl(string) {
        try {
            new URL(string);
            return true;
        } catch (_) {
            return false;
        }
    }

    // Escape HTML
    escapeHtml(text) {
        const map = {
            '&': '&amp;',
            '<': '&lt;',
            '>': '&gt;',
            '"': '&quot;',
            "'": '&#039;'
        };
        return text ? text.replace(/[&<>"']/g, (m) => map[m]) : '';
    }

    // Get current theme
    getCurrentTheme() {
        return document.documentElement.getAttribute('data-theme') || 'light';
    }

    // Network status
    isOnline() {
        return navigator.onLine;
    }
}

// Initialize utilities
window.decypharrUtils = new DecypharrUtils();

// Global functions for backward compatibility
window.fetcher = (endpoint, options = {}) => window.decypharrUtils.fetcher(endpoint, options);
window.createToast = (message, type, duration) => window.decypharrUtils.createToast(message, type, duration);

// Export for ES6 modules if needed
if (typeof module !== 'undefined' && module.exports) {
    module.exports = DecypharrUtils;
}