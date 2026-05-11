// Setup Wizard JavaScript
class SetupWizard {
    constructor() {
        this.currentStep = 1;
        this.setupState = {
            step1: {}, // Authentication
            step2: {}, // Debrid
            step3: {}, // Usenet
            step4: {}, // Download Folder
            step5: {}, // Mount System
            step6: {}, // Overview
        };

        this.refs = {
            loadingState: document.getElementById('loading-state'),
            errorState: document.getElementById('error-state'),
            errorMessage: document.getElementById('error-message'),
        };

        this.init();
    }

    init() {
        this.initializeWizard();
    }

    async initializeWizard() {
        try {
            // Load existing config to populate fields
            await this.loadExistingConfig();

            this.hideLoading();
            this.showStep(this.currentStep);
            this.setupEventListeners();
        } catch (error) {
            console.error('Initialization error:', error);
            this.showError('Failed to initialize setup wizard: ' + error.message);
            this.hideLoading();
        }
    }

    async loadExistingConfig() {
        try {
            const response = await window.decypharrUtils.fetcher('/api/config');
            if (response.ok) {
                const config = await response.json();
                this.populateFields(config);
            }
        } catch (error) {
            console.error('Failed to load config:', error);
            // Continue without pre-populating
        }
    }

    populateFields(config) {
        // Populate authentication fields
        if (config.auth_username) {
            document.getElementById('auth-username').value = config.auth_username;
        }

        // Populate debrid fields if exists
        if (config.debrids && config.debrids.length > 0) {
            const debrid = config.debrids[0];
            document.getElementById('debrid-provider').value = debrid.provider || '';
            document.getElementById('debrid-api-key').value = debrid.api_key || '';
        }

        // Populate usenet fields if exists
        if (config.usenet && config.usenet.length > 0) {
            const usenet = config.usenet[0];
            document.getElementById('usenet-host').value = usenet.host || '';
            document.getElementById('usenet-port').value = usenet.port || '';
            document.getElementById('usenet-username').value = usenet.username || '';
            document.getElementById('usenet-password').value = usenet.password || '';
            document.getElementById('usenet-max-connections').value = usenet.max_connections || '';
            document.getElementById('usenet-max-connections-per-reader').value = usenet.reader_connections || '';
        }

        // Populate download folder
        if (config.download_folder) {
            document.getElementById('download-folder').value = config.download_folder;
        }

        // Populate mount settings
        if (config.dfs?.enabled) {
            document.getElementById('mount-type-dfs').checked = true;
            document.getElementById('mount-path').value = config.dfs.mount_path || '';
            document.getElementById('cache-dir').value = config.dfs.cache_dir || '';
        } else if (config.rclone?.enabled) {
            document.getElementById('mount-type-rclone').checked = true;
            document.getElementById('mount-path').value = config.rclone.mount_path || '';
            document.getElementById('cache-dir').value = config.rclone.vfs_cache_dir || '';
            if (config.rclone.buffer_size) {
                document.getElementById('rclone-buffer-size').value = config.rclone.buffer_size;
            }
        }
    }

    setupEventListeners() {
        document.getElementById('auth-next-btn').addEventListener('click', () => this.handleAuthNext());
        document.getElementById('skip-auth-btn').addEventListener('click', () => this.handleSkipAuth());
        document.getElementById('debrid-back-btn').addEventListener('click', () => this.goToStep(1));
        document.getElementById('debrid-next-btn').addEventListener('click', () => this.handleDebridNext());
        document.getElementById('skip-debrid-btn').addEventListener('click', () => this.handleSkipDebrid());
        document.getElementById('usenet-back-btn').addEventListener('click', () => this.goToStep(2));
        document.getElementById('usenet-next-btn').addEventListener('click', () => this.handleUsenetNext());
        document.getElementById('skip-usenet-btn').addEventListener('click', () => this.handleSkipUsenet());
        document.getElementById('download-back-btn').addEventListener('click', () => this.goToStep(3));
        document.getElementById('download-next-btn').addEventListener('click', () => this.handleDownloadNext());
        document.getElementById('mount-back-btn').addEventListener('click', () => this.goToStep(4));
        document.getElementById('mount-next-btn').addEventListener('click', () => this.handleMountNext());
        document.getElementById('mount-type-dfs').addEventListener('change', () => this.toggleMountOptions());
        document.getElementById('mount-type-rclone').addEventListener('change', () => this.toggleMountOptions());
        document.getElementById('mount-type-external').addEventListener('change', () => this.toggleMountOptions());
        document.getElementById('mount-type-none').addEventListener('change', () => this.toggleMountOptions());
        document.getElementById('overview-back-btn').addEventListener('click', () => this.goToStep(5));
        document.getElementById('finish-btn').addEventListener('click', () => this.handleFinish());
    }

    showLoading() {
        this.refs.loadingState.classList.remove('hidden');
        document.querySelectorAll('.setup-step').forEach(step => step.classList.add('hidden'));
    }

    hideLoading() {
        this.refs.loadingState.classList.add('hidden');
    }

    showError(message) {
        this.refs.errorMessage.textContent = message;
        this.refs.errorState.classList.remove('hidden');
    }

    hideError() {
        this.refs.errorState.classList.add('hidden');
    }

    goToStep(step) {
        this.currentStep = step;
        this.showStep(step);
    }

    showStep(step) {
        this.hideError();
        document.querySelectorAll('.setup-step').forEach(s => s.classList.add('hidden'));

        const stepElement = document.getElementById(`step-${step}`);
        if (stepElement) {
            stepElement.classList.remove('hidden');
        }

        this.updateProgressIndicators(step);

        if (step === 6) {
            this.populateOverview();
        }
    }

    updateProgressIndicators(currentStep) {
        for (let i = 1; i <= 6; i++) {
            const indicator = document.getElementById(`step-indicator-${i}`);
            if (i <= currentStep) {
                indicator.classList.add('step-primary');
            } else {
                indicator.classList.remove('step-primary');
            }
        }
    }

    handleAuthNext() {
        const username = document.getElementById('auth-username').value.trim();
        const password = document.getElementById('auth-password').value;
        const confirmPassword = document.getElementById('auth-confirm-password').value;

        if (username || password || confirmPassword) {
            if (!username) {
                this.showError('Username is required');
                return;
            }
            if (!password) {
                this.showError('Password is required');
                return;
            }
            if (password !== confirmPassword) {
                this.showError('Passwords do not match');
                return;
            }
            if (password.length < 6) {
                this.showError('Password must be at least 6 characters');
                return;
            }

            this.setupState.step1 = {
                username: username,
                password: password,
                skip_auth: false,
            };
        } else {
            this.setupState.step1 = {skip_auth: true};
        }

        this.goToStep(2);
    }

    handleSkipAuth() {
        this.setupState.step1 = {skip_auth: true};
        this.goToStep(2);
    }

    handleDebridNext() {
        const provider = document.getElementById('debrid-provider').value;
        const apiKey = document.getElementById('debrid-api-key').value.trim();

        if (!provider) {
            this.showError('Please select a debrid provider');
            return;
        }
        if (!apiKey) {
            this.showError('API key is required');
            return;
        }

        this.setupState.step2 = {
            provider: provider,
            api_key: apiKey,
        };

        this.goToStep(3);
    }

    handleSkipDebrid() {
        this.setupState.step2 = {skip_debrid: true};
        this.goToStep(3);
    }

    handleUsenetNext() {
        const host = document.getElementById('usenet-host').value.trim();
        const port = document.getElementById('usenet-port').value.trim();
        const username = document.getElementById('usenet-username').value.trim();
        const password = document.getElementById('usenet-password').value.trim();
        const maxConnections = document.getElementById('usenet-max-connections').value.trim();
        const maxConnectionsPerReader = document.getElementById('usenet-max-connections-per-reader').value.trim();

        // If any field is filled, validate all required fields
        if (host || port || username || password) {
            if (!host) {
                this.showError('Usenet server host is required');
                return;
            }
            if (!port) {
                this.showError('Usenet server port is required');
                return;
            }
            if (!username) {
                this.showError('Usenet username is required');
                return;
            }
            if (!password) {
                this.showError('Usenet password is required');
                return;
            }

            // Validate port number
            const portNum = parseInt(port);
            if (isNaN(portNum) || portNum < 1 || portNum > 65535) {
                this.showError('Please enter a valid port number (1-65535)');
                return;
            }

            // Validate max connections if provided
            if (maxConnections) {
                const maxConns = parseInt(maxConnections);
                if (isNaN(maxConns) || maxConns < 1 || maxConns > 50) {
                    this.showError('Max connections must be between 1 and 50');
                    return;
                }
            }

            if (maxConnectionsPerReader) {
                const maxConnsPerReader = parseInt(maxConnectionsPerReader);
                if (isNaN(maxConnsPerReader) || maxConnsPerReader < 1 || maxConnsPerReader > 50) {
                    this.showError('Connections per stream must be between 1 and 50');
                    return;
                }
            }

            this.setupState.step3 = {
                host: host,
                port: parseInt(port),
                username: username,
                password: password,
                max_connections: maxConnections ? parseInt(maxConnections) : 30,
                reader_connections: maxConnectionsPerReader ? parseInt(maxConnectionsPerReader) : 15,
                skip_usenet: false,
            };
        } else {
            // All fields empty - skip usenet
            this.setupState.step3 = {skip_usenet: true};
        }

        if (!this.ensureProviderRequirement()) {
            return;
        }

        this.goToStep(4);
    }

    handleSkipUsenet() {
        this.setupState.step3 = {skip_usenet: true};
        if (!this.ensureProviderRequirement()) {
            return;
        }

        this.goToStep(4);
    }

    handleDownloadNext() {
        const downloadFolder = document.getElementById('download-folder').value.trim();

        if (!downloadFolder) {
            this.showError('Download folder is required');
            return;
        }

        this.setupState.step4 = {
            download_folder: downloadFolder,
        };

        this.goToStep(5);
    }

    hasDebridConfigured() {
        return Boolean(
            this.setupState.step2 &&
            !this.setupState.step2.skip_debrid &&
            this.setupState.step2.provider &&
            this.setupState.step2.api_key
        );
    }

    hasUsenetConfigured() {
        return Boolean(
            this.setupState.step3 &&
            !this.setupState.step3.skip_usenet &&
            this.setupState.step3.host &&
            this.setupState.step3.port &&
            this.setupState.step3.username &&
            this.setupState.step3.password
        );
    }

    ensureProviderRequirement() {
        if (this.hasDebridConfigured() || this.hasUsenetConfigured()) {
            return true;
        }

        this.showError('Please configure at least one Debrid or Usenet provider before continuing.');
        return false;
    }

    toggleMountOptions() {
        const isDFS = document.getElementById('mount-type-dfs').checked;
        const isRclone = document.getElementById('mount-type-rclone').checked;
        const isNone = document.getElementById('mount-type-none').checked;
        const rcloneOptions = document.getElementById('rclone-options');

        if (isDFS || isNone) {
            rcloneOptions.classList.add('hidden');
        } else {
            rcloneOptions.classList.remove('hidden');
        }
    }

    handleMountNext() {
        const mountType = document.querySelector('input[name="mount_type"]:checked').value;
        const mountPath = document.getElementById('mount-path').value.trim();
        const cacheDir = document.getElementById('cache-dir').value.trim();
        const rcloneBufferSize = document.getElementById('rclone-buffer-size').value.trim();

        if (!mountPath && mountType !== 'none') {
            this.showError('Mount path is required');
            return;
        }

        this.setupState.step5 = {
            mount_type: mountType,
            mount_path: mountPath,
            cache_dir: cacheDir,
        };

        if (mountType === 'rclone' && rcloneBufferSize) {
            this.setupState.step5.rclone_buffer_size = rcloneBufferSize;
        }

        this.goToStep(6);
    }

    populateOverview() {
        const authOverview = document.getElementById('overview-auth');
        if (this.setupState.step1 && this.setupState.step1.skip_auth) {
            authOverview.textContent = 'Authentication disabled (skipped)';
        } else if (this.setupState.step1 && this.setupState.step1.username) {
            authOverview.textContent = `Username: ${this.setupState.step1.username}`;
        } else {
            authOverview.textContent = 'Not configured';
        }

        const debridOverview = document.getElementById('overview-debrid');
        if (this.setupState.step2 && this.setupState.step2.skip_debrid) {
            debridOverview.textContent = 'Debrid disabled (skipped)';
        } else if (this.setupState.step2 && this.setupState.step2.provider) {
            debridOverview.innerHTML = `
                <p><strong>Provider:</strong> ${this.setupState.step2.provider}</p>
            `;
        } else {
            debridOverview.textContent = 'Not configured';
        }

        const usenetOverview = document.getElementById('overview-usenet');
        if (this.setupState.step3 && this.setupState.step3.skip_usenet) {
            usenetOverview.textContent = 'Usenet disabled (skipped)';
        } else if (this.setupState.step3 && this.setupState.step3.host) {
            usenetOverview.innerHTML = `
                <p><strong>Server:</strong> ${this.setupState.step3.host}:${this.setupState.step3.port}</p>
                <p><strong>Username:</strong> ${this.setupState.step3.username}</p>
                <p><strong>Max Connections:</strong> ${this.setupState.step3.max_connections}</p>
                <p><strong>Connections Per Stream:</strong> ${this.setupState.step3.reader_connections}</p>
            `;
        } else {
            usenetOverview.textContent = 'Not configured';
        }

        const downloadOverview = document.getElementById('overview-download');
        downloadOverview.textContent = (this.setupState.step4 && this.setupState.step4.download_folder) || 'Not set';

        const mountOverview = document.getElementById('overview-mount');
        if (this.setupState.step5 && this.setupState.step5.mount_type) {
            let mountType = 'None';
            if (this.setupState.step5.mount_type === 'rclone') {
                mountType = 'Rclone';
            } else if (this.setupState.step5.mount_type === 'external_rclone') {
                mountType = 'External Rclone';
            } else if (this.setupState.step5.mount_type === 'dfs') {
                mountType = 'DFS (Decypharr File System)';
            }
            mountOverview.innerHTML = `
                <p><strong>Type:</strong> ${mountType}</p>
                <p><strong>Mount Path:</strong> ${this.setupState.step5.mount_path}</p>
                <p><strong>Cache Directory:</strong> ${this.setupState.step5.cache_dir}</p>
            `;
        } else {
            mountOverview.textContent = 'Not configured';
        }
    }

    async handleFinish() {
        const finishBtn = document.getElementById('finish-btn');
        const finishBtnText = document.getElementById('finish-btn-text');
        const finishBtnLoading = document.getElementById('finish-btn-loading');

        finishBtn.disabled = true;
        finishBtnText.classList.add('hidden');
        finishBtnLoading.classList.remove('hidden');

        if (!this.ensureProviderRequirement()) {
            finishBtn.disabled = false;
            finishBtnText.classList.remove('hidden');
            finishBtnLoading.classList.add('hidden');
            return;
        }

        try {
            // Collect all data
            const setupData = {
                auth: this.setupState.step1,
                debrid: this.setupState.step2,
                usenet: this.setupState.step3,
                download: this.setupState.step4,
                mount: this.setupState.step5,
            };

            const response = await window.decypharrUtils.fetcher('/api/setup/complete', {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify(setupData),
            });

            if (!response.ok) {
                const errorData = await response.json().catch(() => ({error: 'Failed to complete setup'}));
                throw new Error(errorData.error || 'Failed to complete setup');
            }

            const data = await response.json();
            window.decypharrUtils.createToast('Setup completed successfully! Redirecting...', 'success');

            setTimeout(() => {
                window.location.href = window.decypharrUtils.joinURL(window.urlBase, '/');
            }, 1500);

        } catch (error) {
            console.error('Finish error:', error);
            this.showError(error.message || 'Failed to complete setup');
            finishBtn.disabled = false;
            finishBtnText.classList.remove('hidden');
            finishBtnLoading.classList.add('hidden');
        }
    }
}
