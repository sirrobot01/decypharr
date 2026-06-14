// Configuration management for Decypharr
class ConfigManager {
    constructor() {
        this.debridCount = 0;
        this.arrCount = 0;
        this.usenetProviderCount = 0;
        this.debridDirectoryCounts = {};
        this.directoryFilterCounts = {};
        this.virtualFolderCount = 0;

        this.refs = {
            configForm: document.getElementById('configForm'),
            loadingOverlay: document.getElementById('loadingOverlay'),
            debridConfigs: document.getElementById('debridConfigs'),
            arrConfigs: document.getElementById('arrConfigs'),
            virtualFoldersContainer: document.getElementById('virtualFoldersContainer'),
            usenetProviders: document.getElementById('usenetProviders'),
            addDebridBtn: document.getElementById('addDebridBtn'),
            addArrBtn: document.getElementById('addArrBtn'),
            addVirtualFolderBtn: document.getElementById('addVirtualFolderBtn'),
            addUsenetProviderBtn: document.getElementById('addUsenetProviderBtn')
        };

        this.init();
    }

    init() {
        this.bindEvents();
        this.loadConfiguration();
        this.setupMagnetHandler();
        this.checkIncompleteConfig();
    }

    checkIncompleteConfig() {
        const urlParams = new URLSearchParams(window.location.search);
        if (urlParams.has('inco')) {
            const errMsg = urlParams.get('inco');
            window.decypharrUtils.createToast(`Incomplete configuration: ${errMsg}`, 'warning');
        }
    }

    bindEvents() {
        // Form submission
        this.refs.configForm.addEventListener('submit', (e) => this.saveConfiguration(e));

        // Add buttons
        this.refs.addDebridBtn.addEventListener('click', () => this.addDebridConfig());
        this.refs.addArrBtn.addEventListener('click', () => this.addArrConfig());
        this.refs.addVirtualFolderBtn.addEventListener('click', () => this.addVirtualFolder());
        this.refs.addUsenetProviderBtn.addEventListener('click', () => this.addUsenetProvider());
    }

    async loadConfiguration() {
        try {
            const response = await window.decypharrUtils.fetcher('/api/config');
            if (!response.ok) {
                throw new Error('Failed to load configuration');
            }

            const config = await response.json();
            this.populateForm(config);

        } catch (error) {
            console.error('Error loading configuration:', error);
            window.decypharrUtils.createToast('Error loading configuration', 'error');
        }
    }

    populateForm(config) {
        // Load general settings
        this.populateGeneralSettings(config);

        // Load debrid configs
        if (config.debrids && Array.isArray(config.debrids)) {
            config.debrids.forEach(debrid => this.addDebridConfig(debrid));
        }

        // Load usenet config
        if (config.usenet) {
            this.populateUsenetSettings(config.usenet);
        }

        // Load virtual folders
        if (config.custom_folders) {
            this.populateVirtualFolders(config.custom_folders);
        }

        // Load Arr configs
        if (config.arrs && Array.isArray(config.arrs)) {
            config.arrs.forEach(arr => this.addArrConfig(arr));
        }

        // Load rclone config
        this.populateMountSettings(config.mount);

        // Load API token info
        this.populateAPIToken(config);

        // Load notifications config
        this.populateNotificationSettings(config.notifications);

        // Load repair config
        this.populateRepairSettings(config.repair, config.arrs);
    }

    populateRepairSettings(repair, arrs) {
        // Always refresh the arrs multi-select so it tracks the latest *Arrs config.
        const arrsSelect = document.getElementById('repair.arrs');
        if (arrsSelect) {
            const wanted = new Set((repair && Array.isArray(repair.arrs)) ? repair.arrs : []);
            arrsSelect.innerHTML = '';
            for (const a of (arrs || [])) {
                if (!a || !a.name) continue;
                const opt = document.createElement('option');
                opt.value = a.name;
                opt.textContent = a.name;
                if (wanted.has(a.name)) opt.selected = true;
                arrsSelect.appendChild(opt);
            }
        }

        if (!repair) return;
        const $ = (id) => document.getElementById(id);
        if ($('repair.enabled')) $('repair.enabled').checked = !!repair.enabled;
        if ($('repair.source')) $('repair.source').value = repair.source || 'arr';
        if ($('repair.schedule')) $('repair.schedule').value = repair.schedule || '';
        if ($('repair.recheck_interval')) $('repair.recheck_interval').value = repair.recheck_interval || '';
        if ($('repair.workers')) $('repair.workers').value = repair.workers || 5;
        if ($('repair.nntp_connection_percent')) $('repair.nntp_connection_percent').value = repair.nntp_connection_percent || 20;
        if ($('repair.strategy')) $('repair.strategy').value = repair.strategy || 'per_entry';
        if ($('repair.auto_repair')) $('repair.auto_repair').checked = !!repair.auto_repair;
        if ($('repair.notify_on_complete')) $('repair.notify_on_complete').checked = !!repair.notify_on_complete;
    }

    collectRepairConfig() {
        const $ = (id) => document.getElementById(id);
        const arrsSelect = $('repair.arrs');
        const arrs = arrsSelect
            ? Array.from(arrsSelect.selectedOptions).map((o) => o.value).filter(Boolean)
            : [];
        return {
            enabled: $('repair.enabled')?.checked || false,
            source: $('repair.source')?.value || 'arr',
            schedule: $('repair.schedule')?.value.trim() || '',
            recheck_interval: $('repair.recheck_interval')?.value.trim() || '',
            workers: parseInt($('repair.workers')?.value, 10) || 0,
            nntp_connection_percent: parseInt($('repair.nntp_connection_percent')?.value, 10) || 0,
            strategy: $('repair.strategy')?.value || 'per_entry',
            auto_repair: $('repair.auto_repair')?.checked || false,
            notify_on_complete: $('repair.notify_on_complete')?.checked || false,
            arrs,
        };
    }

    populateGeneralSettings(config) {
        const fields = [
            'log_level', 'url_base', 'bind_address', 'port',
            'min_file_size', 'max_file_size', 'remove_stalled_after',
            'nzb_user_agent', 'download_folder', 'refresh_interval',
            'max_downloads', 'skip_pre_cache', 'always_rm_tracker_urls',
            'folder_naming', 'refresh_dirs', 'disable_webdav',
            'default_download_action', 'app_url'
        ];

        fields.forEach(field => {
            const element = document.querySelector(`[name="${field}"]`);
            if (element && config[field] !== undefined) {
                // Handle checkboxes
                if (element.type === 'checkbox') {
                    element.checked = config[field];
                } else {
                    element.value = config[field];
                }
            }
        });

        // Handle allowed file types (array)
        if (config.allowed_file_types && Array.isArray(config.allowed_file_types)) {
            document.querySelector('[name="allowed_file_types"]').value = config.allowed_file_types.join(', ');
        }

        // Set up downloader section toggle
        this.setupDownloaderToggle();
    }

    setupDownloaderToggle() {
        const actionSelect = document.getElementById('default_download_action');
        if (!actionSelect) return;

        const toggleDownloaderOptions = () => {
            const isDownload = actionSelect.value === 'download';
            document.querySelectorAll('.downloader-option').forEach(el => {
                el.disabled = !isDownload;
                el.closest('div').classList.toggle('opacity-50', !isDownload);
            });
        };

        actionSelect.addEventListener('change', toggleDownloaderOptions);
        toggleDownloaderOptions();
    }

    populateNotificationSettings(notificationsConfig) {
        if (!notificationsConfig) return;

        // Handle enabled checkbox
        const enabledElement = document.getElementById('notifications.enabled');
        if (enabledElement) {
            enabledElement.checked = notificationsConfig.enabled || false;
        }

        // Handle webhook URL
        const webhookElement = document.getElementById('notifications.webhook_url');
        if (webhookElement && notificationsConfig.webhook_url) {
            webhookElement.value = notificationsConfig.webhook_url;
        }

        // Handle callback URL
        const callbackElement = document.getElementById('notifications.callback_url');
        if (callbackElement && notificationsConfig.callback_url) {
            callbackElement.value = notificationsConfig.callback_url;
        }

        // Handle events checkboxes
        if (notificationsConfig.events && Array.isArray(notificationsConfig.events)) {
            notificationsConfig.events.forEach(event => {
                const checkbox = document.querySelector(`input[name="notifications.events[]"][value="${event}"]`);
                if (checkbox) {
                    checkbox.checked = true;
                }
            });
        }
    }

    populateMountSettings(mountConfig) {
        if (!mountConfig) return;

        // Handle mount type radio buttons
        if (mountConfig.type) {
            const typeRadio = document.querySelector(`input[name="mount.type"][value="${mountConfig.type}"]`);
            if (typeRadio) {
                typeRadio.checked = true;
                // Trigger change event to switch to the correct tab
                typeRadio.dispatchEvent(new Event('change'));
            }
        }

        // Handle mount path
        const mountPathElement = document.querySelector('[name="mount.mount_path"]');
        if (mountPathElement && mountConfig.mount_path !== undefined) {
            mountPathElement.value = mountConfig.mount_path;
        }

        // Then populate specific mount settings based on type
        this.populateRcloneSettings(mountConfig.rclone);
        this.populateDFSSettings(mountConfig.dfs);
        this.populateExternalRcloneSettings(mountConfig.external_rclone);


    }

    populateRcloneSettings(rcloneConfig) {
        if (!rcloneConfig) return;

        const fields = [
            'port', 'cache_dir', 'transfers', 'vfs_cache_mode', 'vfs_cache_max_size', 'vfs_cache_max_age',
            'vfs_cache_poll_interval', 'vfs_read_chunk_size', 'vfs_read_chunk_size_limit', 'buffer_size', 'bw_limit',
            'uid', 'gid', 'vfs_read_ahead', 'attr_timeout', 'dir_cache_time', 'poll_interval', 'umask',
            'no_modtime', 'no_checksum', 'log_level', 'vfs_cache_min_free_space', 'vfs_fast_fingerprint', 'vfs_read_chunk_streams',
            'async_read', 'use_mmap'
        ];

        fields.forEach(field => {
            const element = document.querySelector(`[name="mount.rclone.${field}"]`);
            if (element && rcloneConfig[field] !== undefined) {
                if (element.type === 'checkbox') {
                    element.checked = rcloneConfig[field];
                } else {
                    element.value = rcloneConfig[field];
                }
            }
        });
    }

    populateDFSSettings(dfsConfig) {
        if (!dfsConfig) return;

        const fields = [
            'cache_dir', 'disk_cache_size', 'cache_expiry', 'cache_cleanup_interval',
            'chunk_size', 'read_ahead_size', 'daemon_timeout',
            'uid', 'gid', 'umask'
        ];

        fields.forEach(field => {
            const element = document.querySelector(`[name="mount.dfs.${field}"]`);
            if (element && dfsConfig[field] !== undefined) {
                if (element.type === 'checkbox') {
                    element.checked = dfsConfig[field];
                } else {
                    element.value = dfsConfig[field];
                }
            }
        });
    }

    populateExternalRcloneSettings(externalRcloneConfig) {
        if (!externalRcloneConfig) return;
        const fields = ['rc_url', 'rc_username', 'rc_password'];
        fields.forEach(field => {
            const element = document.querySelector(`[name="mount.external_rclone.${field}"]`);
            if (element && externalRcloneConfig[field] !== undefined) {
                element.value = externalRcloneConfig[field];
            }
        });
    }

    addDebridConfig(data = {}) {
        const debridHtml = this.getDebridTemplate(this.debridCount, data);
        this.refs.debridConfigs.insertAdjacentHTML('beforeend', debridHtml);

        // Initialize WebDAV toggle for this debrid
        const newDebrid = this.refs.debridConfigs.lastElementChild;

        // Add event listener to name input for real-time updates
        const nameInput = newDebrid.querySelector(`[name="debrid[${this.debridCount}].name"]`);
        if (nameInput) {
            nameInput.addEventListener('blur', () => {
                this.updateArrDebridDropdowns();
            });
        }

        // Add event listener to delete button for cleanup
        const deleteBtn = newDebrid.querySelector('.btn-error');
        if (deleteBtn) {
            deleteBtn.addEventListener('click', () => {
                // Small delay to allow DOM update before refreshing dropdowns
                setTimeout(() => {
                    this.updateArrDebridDropdowns();
                }, 100);
            });
        }

        // Populate data if provided
        if (Object.keys(data).length > 0) {
            this.populateDebridData(this.debridCount, data);
        }

        // Initialize directory management
        this.debridDirectoryCounts[this.debridCount] = 0;

        // Add directories if they exist
        if (data.directories) {
            Object.entries(data.directories).forEach(([dirName, dirData]) => {
                const dirIndex = this.addDirectory(this.debridCount, {name: dirName, ...dirData});

                // Add filters if available
                if (dirData.filters) {
                    Object.entries(dirData.filters).forEach(([filterType, filterValue]) => {
                        this.addFilter(this.debridCount, dirIndex, filterType, filterValue);
                    });
                }
            });
        }

        this.debridCount++;

        // Update all Arr config debrid dropdowns
        this.updateArrDebridDropdowns();
    }

    populateDebridData(index, data) {
        Object.entries(data).forEach(([key, value]) => {
            const input = document.querySelector(`[name="debrid[${index}].${key}"]`);
            if (input) {
                if (input.type === 'checkbox') {
                    input.checked = value;
                } else if (key === 'download_api_keys' && Array.isArray(value)) {
                    input.value = value.join('\n');
                    // Apply masking to populated textarea
                    if (input.tagName.toLowerCase() === 'textarea') {
                        input.style.webkitTextSecurity = 'disc';
                        input.style.textSecurity = 'disc';
                        input.setAttribute('data-password-visible', 'false');
                    }
                } else {
                    input.value = value;
                }
            }
        });
    }

    getDebridTemplate(index, data = {}) {
        return `
        <div class="card bg-base-100 border border-base-300 shadow-sm debrid-config" data-index="${index}">
            <div class="card-body">
                <div class="flex justify-between items-start mb-4">
                    <h3 class="card-title text-lg">
                        <i class="bi bi-cloud mr-2 text-secondary"></i>
                        Debrid #${index + 1}
                    </h3>
                    <button type="button" class="btn btn-error btn-sm" onclick="this.closest('.debrid-config').remove();">
                        <i class="bi bi-trash"></i>
                    </button>
                </div>
                <div class="grid grid-cols-1 lg:grid-cols-3 gap-6">
                        <div>
                            <label class="label" for="debrid[${index}].name">
                                <span class=" font-medium">Service Type</span>
                            </label>
                            <select class="select w-full" name="debrid[${index}].provider" id="debrid[${index}].provider" required>
                                <option value="realdebrid">Real Debrid</option>
                                <option value="alldebrid">AllDebrid</option>
                                <option value="debridlink">Debrid Link</option>
                                <option value="torbox">Torbox</option>
                                <option value="premiumize">Premiumize</option>
                            </select>
                        </div>
                        
                        <div>
                            <label class="label" for="debrid[${index}].name">
                                <span class=" font-medium">Name</span>
                            </label>
                            <input type="text" class="input w-full" 
                                   name="debrid[${index}].name" id="debrid[${index}].name" 
                                   placeholder="realdebrid">
                            <span class="text-sm opacity-70">A unique name for this debrid account</span>
                        </div>

                        <div>
                            <label class="label" for="debrid[${index}].api_key">
                                <span class=" font-medium">API Key</span>
                            </label>
                            <div class="password-toggle-container">
                                <input type="password" class="input input-has-toggle" 
                                       name="debrid[${index}].api_key" id="debrid[${index}].api_key" required>
                                <button type="button" class="password-toggle-btn">
                                    <i class="bi bi-eye" id="debrid[${index}].api_key_icon"></i>
                                </button>
                            </div>
                           <span class="text-sm opacity-70">API key for the debrid service</span>
                        </div>
                </div>

                <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
                    <div class="flex flex-col">
                        <div class="fieldset flex-1">
                            <label class="label" for="debrid[${index}].download_api_keys">
                                <span class=" font-medium">Download API Keys</span>
                            </label>
                            <div class="password-toggle-container">
                                <textarea class="textarea has-toggle font-mono h-full min-h-[200px]" 
                                          name="debrid[${index}].download_api_keys" 
                                          id="debrid[${index}].download_api_keys" 
                                          placeholder="Multiple API keys for download (one per line). If empty, main API key will be used."></textarea>
                                <button type="button" class="password-toggle-btn textarea-toggle">
                                    <i class="bi bi-eye" id="debrid[${index}].download_api_keys_icon"></i>
                                </button>
                            </div>
                            <span class="text-sm opacity-70">Multiple API keys for downloads - leave empty to use main API key</span>
                        </div>
                    </div>
                    <div class="space-y-4">
                        <div class="grid grid-cols-2 lg:grid-cols-3 gap-3">
                            <div>
                                <label class="label" for="debrid[${index}].rate_limit">
                                    <span class=" font-medium">Rate Limit</span>
                                </label>
                                <input type="text" class="input w-full" 
                                       name="debrid[${index}].rate_limit" id="debrid[${index}].rate_limit" 
                                       placeholder="250/minute" value="250/minute">
                                <span class="text-sm opacity-70">API rate limit for this service</span>
                            </div>
                            <div>
                                <label class="label" for="debrid[${index}].repair_rate_limit">
                                    <span class=" font-medium">Repair Rate Limit</span>
                                </label>
                                <input type="text" class="input w-full" 
                                       name="debrid[${index}].repair_rate_limit" id="debrid[${index}].repair_rate_limit" 
                                       placeholder="100/minute">
                                <span class="text-sm opacity-70">API rate limit for repair operations</span>
                            </div>
                            <div>
                                <label class="label" for="debrid[${index}].download_rate_limit">
                                    <span class=" font-medium">Download Rate Limit</span>
                                </label>
                                <input type="text" class="input w-full" 
                                       name="debrid[${index}].download_rate_limit" id="debrid[${index}].download_rate_limit" 
                                       placeholder="150/minute">
                                <span class="text-sm opacity-70">API rate limit for download operations</span>
                            </div>
                            <div>
                                <label class="label" for="debrid[${index}].proxy">
                                    <span class=" font-medium">Proxy</span>
                                </label>
                                <input type="text" class="input w-full" 
                                       name="debrid[${index}].proxy" id="debrid[${index}].proxy" 
                                       placeholder="socks4, socks5, https proxy">
                                <span class="text-sm opacity-70">This proxy is used for this debrid account</span>
                            </div>
                            <div>
                                <label class="label" for="debrid[${index}].user_agent">
                                    <span class=" font-medium">Custom User Agent</span>
                                </label>
                                <input type="text" class="input w-full" 
                                       name="debrid[${index}].user_agent" id="debrid[${index}].user_agent" 
                                       placeholder="Decypharr/1.0">
                                <span class="text-sm opacity-70">Custom User Agent for this debrid</span>
                            </div>
                            <div>
                                <label class="label" for="debrid[${index}].minimum_free_slot">
                                    <span class=" font-medium">Minimum Free Slot</span>
                                </label>
                                <input type="number" class="input w-full" 
                                       name="debrid[${index}].minimum_free_slot" id="debrid[${index}].minimum_free_slot" 
                                       placeholder="1" value="1">
                                <span class="text-sm opacity-70">Minimum free slot for this debrid</span>
                            </div>
                        </div>
                    </div>
                </div>
                
                <div class="grid grid-cols-1 lg:grid-cols-3 gap-6">
                    <div>
                        <label class="label" for="debrid[${index}].torrents_refresh_interval">
                            <span class=" font-medium">Torrents Refresh Interval</span>
                        </label>
                        <input type="text" class="input webdav-field" 
                               name="debrid[${index}].torrents_refresh_interval" 
                               id="debrid[${index}].torrents_refresh_interval" 
                               placeholder="10m" value="10m">
                        <span class="text-sm opacity-70">How often to refresh torrents list</span>
                    </div>
                    <div>
                        <label class="label" for="debrid[${index}].download_links_refresh_interval">
                            <span class=" font-medium">Links Refresh Interval</span>
                        </label>
                        <input type="text" class="input webdav-field" 
                               name="debrid[${index}].download_links_refresh_interval" 
                               id="debrid[${index}].download_links_refresh_interval" 
                               placeholder="40m" value="40m">
                        <span class="text-sm opacity-70">How often to refresh download links</span>
                    </div>
                    <div>
                        <label class="label" for="debrid[${index}].auto_expire_links_after">
                            <span class=" font-medium">Links Expiry</span>
                        </label>
                        <input type="text" class="input webdav-field" 
                               name="debrid[${index}].auto_expire_links_after" 
                               id="debrid[${index}].auto_expire_links_after" 
                               placeholder="3d" value="3d">
                        <span class="text-sm opacity-70">Automatically expire links after this duration</span>
                    </div>
                </div>
                <div class="grid grid-cols-2 lg:grid-cols-3 gap-4 mt-6">
                    <div>
                        <label class="label cursor-pointer justify-start gap-2">
                            <input type="checkbox" class="checkbox checkbox-primary" 
                                   name="debrid[${index}].download_uncached" id="debrid[${index}].download_uncached">
                            <div>
                                <span class="font-medium">Download Uncached</span>
                                <div class="label-text-alt">Download uncached files</div>
                            </div>
                        </label>
                    </div>

                    <div>
                        <label class="label cursor-pointer justify-start gap-2">
                            <input type="checkbox" class="checkbox checkbox-primary" 
                                   name="debrid[${index}].add_samples" id="debrid[${index}].add_samples">
                             <div>
                                <span class=" font-medium">Add Samples</span>
                                <div class="label-text-alt">Include sample files</div>
                            </div>
                        </label>
                    </div>

                    <div>
                        <label class="label cursor-pointer justify-start gap-2">
                            <input type="checkbox" class="checkbox checkbox-primary" 
                                   name="debrid[${index}].unpack_rar" id="debrid[${index}].unpack_rar">
                             <div>
                                <span class="font-medium">Unpack RAR</span>
                                <div class="label-text-alt">Preprocess RAR files</div>
                            </div>
                        </label>
                    </div>
                </div>
        </div>
    `;
    }

    addDirectory(debridIndex, data = {}) {
        if (!this.debridDirectoryCounts[debridIndex]) {
            this.debridDirectoryCounts[debridIndex] = 0;
        }

        const dirIndex = this.debridDirectoryCounts[debridIndex];
        const container = document.getElementById(`debrid[${debridIndex}].directories`);

        const directoryHtml = this.getDirectoryTemplate(debridIndex, dirIndex);
        container.insertAdjacentHTML('beforeend', directoryHtml);

        // Set up tracking for filters in this directory
        const dirKey = `${debridIndex}-${dirIndex}`;
        this.directoryFilterCounts[dirKey] = 0;

        // Fill with directory name if provided
        if (data.name) {
            const nameInput = document.querySelector(`[name="debrid[${debridIndex}].directory[${dirIndex}].name"]`);
            if (nameInput) nameInput.value = data.name;
        }

        this.debridDirectoryCounts[debridIndex]++;
        return dirIndex;
    }

    getDirectoryTemplate(debridIndex, dirIndex) {
        return `
            <div class="card bg-base-200 border border-base-300 directory-item">
                <div class="card-body">
                    <div class="flex justify-between items-start mb-4">
                        <h5 class="text-lg font-medium">Virtual Directory</h5>
                        <button type="button" class="btn btn-error btn-xs" onclick="this.closest('.directory-item').remove();">
                            <i class="bi bi-trash"></i>
                        </button>
                    </div>

                    <div class="fieldset mb-4">
                        <label class="label">
                            <span class=" font-medium">Directory Name</span>
                        </label>
                        <input type="text" class="input webdav-field"
                               name="debrid[${debridIndex}].directory[${dirIndex}].name"
                               placeholder="Movies, TV Shows, Collections, etc.">
                    </div>

                    <div class="space-y-4">
                        <div class="flex justify-between items-center">
                            <h6 class="font-medium flex items-center">
                                Filters
                                <button type="button" class="btn btn-ghost btn-xs ml-2" onclick="configManager.showFilterHelp();">
                                    <i class="bi bi-question-circle"></i>
                                </button>
                            </h6>
                        </div>

                        <div class="filters-container space-y-2" id="debrid[${debridIndex}].directory[${dirIndex}].filters">
                        </div>

                        <div class="flex flex-wrap gap-2">
                            <div class="dropdown">
                                <div tabindex="0" role="button" class="btn btn-outline btn-sm">
                                    <i class="bi bi-plus mr-1"></i>Text Filter
                                    <i class="bi bi-chevron-down ml-1"></i>
                                </div>
                                <ul tabindex="0" class="dropdown-content menu bg-base-100 rounded-box z-[1] w-48 p-2 shadow">
                                    <li><a onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'include');">Include</a></li>
                                    <li><a onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'exclude');">Exclude</a></li>
                                    <li><a onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'starts_with');">Starts With</a></li>
                                    <li><a onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'not_starts_with');">Not Starts With</a></li>
                                    <li><a onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'ends_with');">Ends With</a></li>
                                    <li><a onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'not_ends_with');">Not Ends With</a></li>
                                    <li><a onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'exact_match');">Exact Match</a></li>
                                    <li><a onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'not_exact_match');">Not Exact Match</a></li>
                                </ul>
                            </div>

                            <div class="dropdown">
                                <div tabindex="0" role="button" class="btn btn-outline btn-sm">
                                    <i class="bi bi-code mr-1"></i>Regex Filter
                                    <i class="bi bi-chevron-down ml-1"></i>
                                </div>
                                <ul tabindex="0" class="dropdown-content menu bg-base-100 rounded-box z-[1] w-48 p-2 shadow">
                                    <li><a onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'regex');">Regex Match</a></li>
                                    <li><a onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'not_regex');">Regex Doesn't Match</a></li>
                                </ul>
                            </div>

                            <div class="dropdown">
                                <div tabindex="0" role="button" class="btn btn-outline btn-sm">
                                    <i class="bi bi-hdd mr-1"></i>Size Filter
                                    <i class="bi bi-chevron-down ml-1"></i>
                                </div>
                                <ul tabindex="0" class="dropdown-content menu bg-base-100 rounded-box z-[1] w-48 p-2 shadow">
                                    <li><a onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'size_gt');">Size Greater Than</a></li>
                                    <li><a onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'size_lt');">Size Less Than</a></li>
                                </ul>
                            </div>

                            <button type="button" class="btn btn-outline btn-sm" onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'last_added');">
                                <i class="bi bi-clock mr-1"></i>Last Added Filter
                            </button>
                        </div>
                    </div>
                </div>
            </div>
        `;
    }

    addFilter(debridIndex, dirIndex, filterType, filterValue = "") {
        const dirKey = `${debridIndex}-${dirIndex}`;
        if (!this.directoryFilterCounts[dirKey]) {
            this.directoryFilterCounts[dirKey] = 0;
        }

        const filterIndex = this.directoryFilterCounts[dirKey];
        const container = document.getElementById(`debrid[${debridIndex}].directory[${dirIndex}].filters`);

        if (container) {
            const filterHtml = this.getFilterTemplate(debridIndex, dirIndex, filterIndex, filterType);
            container.insertAdjacentHTML('beforeend', filterHtml);

            // Set filter value if provided
            if (filterValue) {
                const valueInput = container.querySelector(`[name="debrid[${debridIndex}].directory[${dirIndex}].filter[${filterIndex}].value"]`);
                if (valueInput) valueInput.value = filterValue;
            }

            this.directoryFilterCounts[dirKey]++;
        }
    }

    getFilterTemplate(debridIndex, dirIndex, filterIndex, filterType) {
        const filterConfig = this.getFilterConfig(filterType);

        return `
            <div class="filter-item flex items-center gap-3 p-3 bg-base-100 rounded-lg border border-base-300">
                <div class="badge ${filterConfig.badgeClass} badge-sm">
                    ${filterConfig.label}
                </div>
                <input type="hidden"
                       name="debrid[${debridIndex}].directory[${dirIndex}].filter[${filterIndex}].type"
                       value="${filterType}">
                <div class="flex-1">
                    <input type="text" 
                           class="input input-sm w-full webdav-field"
                           name="debrid[${debridIndex}].directory[${dirIndex}].filter[${filterIndex}].value"
                           placeholder="${filterConfig.placeholder}">
                </div>
                <button type="button" class="btn btn-error btn-xs" onclick="this.closest('.filter-item').remove();">
                    <i class="bi bi-x"></i>
                </button>
            </div>
        `;
    }

    getFilterConfig(filterType) {
        const configs = {
            'include': {
                label: 'Include',
                placeholder: 'Text that should be included in filename',
                badgeClass: 'badge-primary'
            },
            'exclude': {
                label: 'Exclude',
                placeholder: 'Text that should not be in filename',
                badgeClass: 'badge-error'
            },
            'regex': {
                label: 'Regex Match',
                placeholder: 'Regular expression pattern',
                badgeClass: 'badge-warning'
            },
            'not_regex': {
                label: 'Regex Not Match',
                placeholder: 'Regular expression pattern that should not match',
                badgeClass: 'badge-error'
            },
            'exact_match': {
                label: 'Exact Match',
                placeholder: 'Exact text to match',
                badgeClass: 'badge-primary'
            },
            'not_exact_match': {
                label: 'Not Exact Match',
                placeholder: 'Exact text that should not match',
                badgeClass: 'badge-error'
            },
            'starts_with': {
                label: 'Starts With',
                placeholder: 'Text that filename starts with',
                badgeClass: 'badge-primary'
            },
            'not_starts_with': {
                label: 'Not Starts With',
                placeholder: 'Text that filename should not start with',
                badgeClass: 'badge-error'
            },
            'ends_with': {
                label: 'Ends With',
                placeholder: 'Text that filename ends with',
                badgeClass: 'badge-primary'
            },
            'not_ends_with': {
                label: 'Not Ends With',
                placeholder: 'Text that filename should not end with',
                badgeClass: 'badge-error'
            },
            'size_gt': {
                label: 'Size Greater Than',
                placeholder: 'Size in bytes, KB, MB, GB (e.g. 700MB)',
                badgeClass: 'badge-success'
            },
            'size_lt': {
                label: 'Size Less Than',
                placeholder: 'Size in bytes, KB, MB, GB (e.g. 700MB)',
                badgeClass: 'badge-warning'
            },
            'last_added': {
                label: 'Added in the last',
                placeholder: 'Time duration (e.g. 24h, 7d, 30d)',
                badgeClass: 'badge-info'
            }
        };

        return configs[filterType] || {
            label: filterType.replace(/_/g, ' ').replace(/\b\w/g, l => l.toUpperCase()),
            placeholder: 'Filter value',
            badgeClass: 'badge-ghost'
        };
    }

    showFilterHelp() {
        // Create and show a modal with filter help
        const modal = document.createElement('dialog');
        modal.className = 'modal';
        modal.innerHTML = `
            <div class="modal-box max-w-2xl">
                <form method="dialog">
                    <button class="btn btn-sm btn-circle btn-ghost absolute right-2 top-2">✕</button>
                </form>
                <h3 class="font-bold text-lg mb-4">Directory Filter Types</h3>
                <div class="space-y-4">
                    <div>
                        <h4 class="font-semibold text-primary">Text Filters</h4>
                        <ul class="list-disc list-inside text-sm space-y-1 ml-4">
                            <li><strong>Include/Exclude:</strong> Simple text inclusion/exclusion</li>
                            <li><strong>Starts/Ends With:</strong> Matches beginning or end of filename</li>
                            <li><strong>Exact Match:</strong> Match the entire filename</li>
                        </ul>
                    </div>
                    <div>
                        <h4 class="font-semibold text-warning">Regex Filters</h4>
                        <ul class="list-disc list-inside text-sm space-y-1 ml-4">
                            <li><strong>Regex:</strong> Use regular expressions for complex patterns</li>
                            <li>Example: <code>.*\\.mkv$</code> matches files ending with .mkv</li>
                        </ul>
                    </div>
                    <div>
                        <h4 class="font-semibold text-success">Size Filters</h4>
                        <ul class="list-disc list-inside text-sm space-y-1 ml-4">
                            <li><strong>Size Greater/Less Than:</strong> Filter by file size</li>
                            <li>Examples: 1GB, 500MB, 2.5GB</li>
                        </ul>
                    </div>
                    <div>
                        <h4 class="font-semibold text-info">Time Filters</h4>
                        <ul class="list-disc list-inside text-sm space-y-1 ml-4">
                            <li><strong>Last Added:</strong> Show only recently added content</li>
                            <li>Examples: 24h, 7d, 30d</li>
                        </ul>
                    </div>
                    <div class="alert alert-info">
                        <i class="bi bi-info-circle"></i>
                        <span>Negative filters (Not...) will exclude matches instead of including them.</span>
                    </div>
                </div>
            </div>
        `;

        document.body.appendChild(modal);
        modal.showModal();

        // Remove modal when closed
        modal.addEventListener('close', () => {
            document.body.removeChild(modal);
        });
    }

    getDebridOptions() {
        // Collect all debrid names from the form
        const debridNames = [];
        const debridConfigs = document.querySelectorAll('.debrid-config');

        debridConfigs.forEach((config) => {
            const index = config.getAttribute('data-index');
            const nameInput = document.querySelector(`[name="debrid[${index}].name"]`);
            if (nameInput && nameInput.value.trim()) {
                debridNames.push(nameInput.value.trim());
            }
        });

        // Generate option elements
        return debridNames.map(name => `<option value="${window.decypharrUtils.escapeHtml(name)}">${window.decypharrUtils.escapeHtml(name)}</option>`).join('');
    }

    updateArrDebridDropdowns() {
        // Update all existing Arr config dropdowns with current debrid services
        const arrConfigs = document.querySelectorAll('.arr-config');
        const debridOptions = this.getDebridOptions();

        arrConfigs.forEach((config) => {
            const index = config.getAttribute('data-index');
            const select = document.querySelector(`[name="arr[${index}].selected_debrid"]`);

            if (select) {
                // Save current selection
                const currentValue = select.value;

                // Update options while preserving "Auto Select"
                select.innerHTML = `<option value="">Auto Select</option>${debridOptions}`;

                // Restore selection if it still exists
                if (currentValue) {
                    select.value = currentValue;
                }
            }
        });
    }

    addArrConfig(data = {}) {
        const arrHtml = this.getArrTemplate(this.arrCount, data);
        this.refs.arrConfigs.insertAdjacentHTML('beforeend', arrHtml);

        // Populate data if provided
        if (Object.keys(data).length > 0) {
            this.populateArrData(this.arrCount, data);
        }

        this.arrCount++;
    }

    populateArrData(index, data) {
        Object.entries(data).forEach(([key, value]) => {
            const input = document.querySelector(`[name="arr[${index}].${key}"]`);
            if (input) {
                if (input.type === 'checkbox') {
                    input.checked = value;
                } else {
                    input.value = value;
                }
            }
        });
    }

    getArrTemplate(index, data = {}) {
        const isAutoDetected = data.source === 'auto';

        // Get available debrid services
        const debridOptions = this.getDebridOptions();

        return `
            <div class="card bg-base-100 border border-base-300 shadow-sm arr-config ${isAutoDetected ? 'border-info' : ''}" data-index="${index}">
                <div class="card-body">
                    <div class="flex justify-between items-start mb-4">
                        <h3 class="card-title text-lg">
                            <i class="bi bi-collection mr-2 text-warning"></i>
                            Arr Service #${index + 1}
                            ${isAutoDetected ? '<div class="badge badge-info badge-sm ml-2">Auto-detected</div>' : ''}
                        </h3>
                        ${!isAutoDetected ? `
                            <button type="button" class="btn btn-error btn-sm" onclick="this.closest('.arr-config').remove();">
                                <i class="bi bi-trash"></i>
                            </button>
                        ` : ''}
                    </div>

                    <input type="hidden" name="arr[${index}].source" value="${data.source || ''}">

                    <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
                        <div>
                            <label class="label" for="arr[${index}].name">
                                <span class=" font-medium">Service Name</span>
                            </label>
                            <input type="text" class="input ${isAutoDetected ? 'input-disabled' : ''}"
                                   name="arr[${index}].name" id="arr[${index}].name"
                                   ${isAutoDetected ? 'readonly' : 'required'}
                                   placeholder="sonarr, radarr, etc.">
                        </div>

                        <div>
                            <label class="label" for="arr[${index}].host">
                                <span class=" font-medium">Host URL</span>
                            </label>
                            <input type="url" class="input ${isAutoDetected ? 'input-disabled' : ''}"
                                   name="arr[${index}].host" id="arr[${index}].host"
                                   ${isAutoDetected ? 'readonly' : 'required'}
                                   placeholder="http://localhost:8989">
                        </div>

                        <div>
                            <label class="label" for="arr[${index}].token">
                                <span class=" font-medium">API Token</span>
                            </label>
                            <div class="password-toggle-container">
                                <input type="password" class="input input-has-toggle ${isAutoDetected ? 'input-disabled' : ''}"
                                       name="arr[${index}].token" id="arr[${index}].token"
                                       ${isAutoDetected ? 'readonly' : 'required'}>
                                <button type="button" class="password-toggle-btn ${isAutoDetected ? 'opacity-50 cursor-not-allowed' : ''}"
                                        ${isAutoDetected ? 'disabled' : ''}>
                                    <i class="bi bi-eye" id="arr[${index}].token_icon"></i>
                                </button>
                            </div>
                        </div>

                        <div>
                            <label class="label" for="arr[${index}].selected_debrid">
                                <span class=" font-medium">Preferred Provider</span>
                            </label>
                            <select class="select w-full" name="arr[${index}].selected_debrid" id="arr[${index}].selected_debrid">
                                <option value="">Auto Select</option>
                                ${debridOptions}
                            </select>
                            <span class="text-sm opacity-70">Which debrid service this Arr should prefer</span>
                        </div>
                    </div>

                    <div class="grid grid-cols-3 gap-4">
                        <div>
                            <label class="label cursor-pointer justify-start gap-2">
                                <input type="checkbox" class="checkbox checkbox-sm checkbox-primary"
                                       name="arr[${index}].cleanup" id="arr[${index}].cleanup">
                                <span class=" text-sm">Cleanup Queue</span>
                            </label>
                        </div>

                        <div>
                            <label class="label cursor-pointer justify-start gap-2">
                                <input type="checkbox" class="checkbox checkbox-sm checkbox-primary"
                                       name="arr[${index}].skip_repair" id="arr[${index}].skip_repair">
                                <span class=" text-sm">Skip Repair</span>
                            </label>
                        </div>

                        <div>
                            <label class="label cursor-pointer justify-start gap-2">
                                <input type="checkbox" class="checkbox checkbox-sm checkbox-primary"
                                       name="arr[${index}].download_uncached" id="arr[${index}].download_uncached">
                                <span class=" text-sm">Download Uncached</span>
                            </label>
                        </div>
                    </div>
                </div>
            </div>
        `;
    }

    async saveConfiguration(e) {
        e.preventDefault();

        // Show loading overlay
        this.refs.loadingOverlay.classList.remove('hidden');

        try {
            const config = this.collectFormData();

            // Validate configuration
            const validation = this.validateConfiguration(config);
            if (!validation.valid) {
                throw new Error(validation.errors.join('\n'));
            }

            const response = await window.decypharrUtils.fetcher('/api/config', {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify(config)
            });

            if (!response.ok) {
                const errorText = await response.text();
                throw new Error(errorText || 'Failed to save configuration');
            }

            window.decypharrUtils.createToast('Configuration saved successfully! Services are restarting...', 'success');

            // Reload page after a delay to allow services to restart
            setTimeout(() => {
                window.location.reload();
            }, 2000);

        } catch (error) {
            console.error('Error saving configuration:', error);
            window.decypharrUtils.createToast(`Error saving configuration: ${error.message}`, 'error');
            this.refs.loadingOverlay.classList.add('hidden');
        }
    }

    validateConfiguration(config) {
        const errors = [];

        // Validate debrid services
        config.debrids.forEach((debrid, index) => {
            if (!debrid.name || !debrid.api_key || !debrid.provider) {
                errors.push(`Debrid service #${index + 1}: Name, API key are required`);
            }
        });

        // Validate Arr services
        config.arrs.forEach((arr, index) => {
            if (!arr.name || !arr.host) {
                errors.push(`Arr service #${index + 1}: Name and host are required`);
            }

            if (arr.host && !this.isValidUrl(arr.host)) {
                errors.push(`Arr service #${index + 1}: Invalid host URL format`);
            }
        });

        if (config.mount.type === "") {
            errors.push('Mount type is required when ');
        }

        if (config.mount.mount_path === "") {
            errors.push('Mount path is required when Rclone is enabled');
        }

        if (config.repair?.enabled && !config.repair.schedule) {
            errors.push('Repair: schedule is required when Repair is enabled');
        }
        return {
            valid: errors.length === 0,
            errors
        };
    }

    isValidUrl(string) {
        try {
            new URL(string);
            return true;
        } catch (_) {
            return false;
        }
    }

    collectFormData() {
        return {
            // General settings
            log_level: document.querySelector('[name="log_level"]').value,
            url_base: document.querySelector('[name="url_base"]').value,
            bind_address: document.querySelector('[name="bind_address"]').value,
            app_url: document.querySelector('[name="app_url"]').value,
            port: document.querySelector('[name="port"]').value,
            allowed_file_types: document.querySelector('[name="allowed_file_types"]').value
                .split(',').map(ext => ext.trim()).filter(Boolean),
            min_file_size: document.querySelector('[name="min_file_size"]').value,
            max_file_size: document.querySelector('[name="max_file_size"]').value,
            remove_stalled_after: document.querySelector('[name="remove_stalled_after"]').value || "10m",
            nzb_user_agent: document.querySelector('[name="nzb_user_agent"]').value,
            download_folder: document.querySelector('[name="download_folder"]').value,
            refresh_interval: document.querySelector('[name="refresh_interval"]').value || "30s",
            default_download_action: document.querySelector('[name="default_download_action"]')?.value || "symlink",
            max_downloads: parseInt(document.querySelector('[name="max_downloads"]').value) || 0,
            skip_pre_cache: document.querySelector('[name="skip_pre_cache"]').checked,
            always_rm_tracker_urls: document.querySelector('[name="always_rm_tracker_urls"]').checked,
            folder_naming: document.querySelector('[name="folder_naming"]')?.value || "",
            disable_webdav: document.querySelector('[name="disable_webdav"]').checked,
            refresh_dirs: document.querySelector('[name="refresh_dirs"]')?.value || "",
            custom_folders: this.collectVirtualFolders(),

            // Debrid configurations
            debrids: this.collectDebridConfigs(),

            // Arr configurations
            arrs: this.collectArrConfigs(),

            // Mount configuration
            mount: this.collectMountConfig(),

            // Collect usenet configs
            usenet: this.collectUsenetConfig(),

            // Collect notifications config
            notifications: this.collectNotificationsConfig(),

            // Collect repair config
            repair: this.collectRepairConfig()
        };
    }

    collectNotificationsConfig() {
        const enabledElement = document.getElementById('notifications.enabled');
        const webhookElement = document.getElementById('notifications.webhook_url');
        const callbackElement = document.getElementById('notifications.callback_url');

        // Collect selected events
        const events = [];
        document.querySelectorAll('input[name="notifications.events[]"]').forEach(checkbox => {
            if (checkbox.checked) {
                events.push(checkbox.value);
            }
        });

        return {
            enabled: enabledElement ? enabledElement.checked : false,
            webhook_url: webhookElement ? webhookElement.value : '',
            callback_url: callbackElement ? callbackElement.value : '',
            events: events
        };
    }

    collectUsenetConfig() {
        const providers = [];

        this.refs.usenetProviders.querySelectorAll('.usenet-provider').forEach((providerCard) => {
            const index = providerCard.getAttribute('data-index');
            const getField = (field) => providerCard.querySelector(`[name="usenet.providers[${index}].${field}"]`);

            const hostInput = getField('host');
            const portInput = getField('port');
            const usernameInput = getField('username');
            const passwordInput = getField('password');
            const backboneInput = getField('backbone');
            const sslInput = getField('ssl');
            const maxConnectionsInput = getField('max_connections');
            const priorityInput = getField('priority');

            if (!hostInput || !portInput || !usernameInput || !passwordInput || !backboneInput || !sslInput || !maxConnectionsInput || !priorityInput) {
                return;
            }

            const provider = {
                host: hostInput.value,
                port: parseInt(portInput.value) || 119,
                username: usernameInput.value,
                password: passwordInput.value,
                backbone: backboneInput.value.trim(),
                ssl: sslInput.checked,
                max_connections: parseInt(maxConnectionsInput.value) || 100,
                priority: parseInt(priorityInput.value) || 0
            };

            if (provider.host && provider.username && provider.password) {
                providers.push(provider);
            }
        });

        return {
            providers: providers,
            max_connections: parseInt(document.querySelector('[name="usenet.max_connections"]')?.value) || 10,
            read_ahead: document.querySelector('[name="usenet.read_ahead"]').value || "32MB",
            processing_timeout: document.querySelector('[name="usenet.processing_timeout"]')?.value || "5m",
            availability_sample_percent: parseInt(document.querySelector('[name="usenet.availability_sample_percent"]')?.value) || 10,
            max_concurrent_nzb: parseInt(document.querySelector('[name="usenet.max_concurrent_nzb"]')?.value) || 2,
            disk_buffer_path: document.querySelector('[name="usenet.disk_buffer_path"]')?.value || "",
            skip_repair: document.querySelector('[name="usenet.skip_repair"]').checked
        };
    }

    collectDebridConfigs() {
        const debrids = [];

        this.refs.debridConfigs.querySelectorAll('.debrid-config').forEach((debridCard) => {
            const index = debridCard.getAttribute('data-index');
            const getField = (field) => debridCard.querySelector(`[name="debrid[${index}].${field}"]`);

            const nameInput = getField('name');
            const providerInput = getField('provider');
            const apiKeyInput = getField('api_key');
            const rateLimitInput = getField('rate_limit');
            const repairRateLimitInput = getField('repair_rate_limit');
            const downloadRateLimitInput = getField('download_rate_limit');
            const minimumFreeSlotInput = getField('minimum_free_slot');
            const proxyInput = getField('proxy');
            const downloadUncachedInput = getField('download_uncached');
            const unpackRarInput = getField('unpack_rar');
            const addSamplesInput = getField('add_samples');
            const userAgentInput = getField('user_agent');
            const downloadKeysTextarea = getField('download_api_keys');
            const torrentsRefreshIntervalInput = getField('torrents_refresh_interval');
            const downloadLinksRefreshIntervalInput = getField('download_links_refresh_interval');
            const autoExpireLinksAfterInput = getField('auto_expire_links_after');

            if (!nameInput || !providerInput || !apiKeyInput || !rateLimitInput || !repairRateLimitInput || !downloadRateLimitInput ||
                !minimumFreeSlotInput || !proxyInput || !downloadUncachedInput || !unpackRarInput || !addSamplesInput ||
                !userAgentInput || !torrentsRefreshIntervalInput || !downloadLinksRefreshIntervalInput || !autoExpireLinksAfterInput) {
                return;
            }

            const debrid = {
                name: nameInput.value,
                provider: providerInput.value,
                api_key: apiKeyInput.value,
                rate_limit: rateLimitInput.value,
                repair_rate_limit: repairRateLimitInput.value,
                download_rate_limit: downloadRateLimitInput.value,
                minimum_free_slot: parseInt(minimumFreeSlotInput.value) || 0,
                proxy: proxyInput.value,
                download_uncached: downloadUncachedInput.checked,
                unpack_rar: unpackRarInput.checked,
                add_samples: addSamplesInput.checked,
                user_agent: userAgentInput.value
            };

            // Handle download API keys
            if (downloadKeysTextarea && downloadKeysTextarea.value.trim()) {
                debrid.download_api_keys = downloadKeysTextarea.value
                    .split('\n')
                    .map(key => key.trim())
                    .filter(key => key.length > 0);
            }

            debrid.torrents_refresh_interval = torrentsRefreshIntervalInput.value;
            debrid.download_links_refresh_interval = downloadLinksRefreshIntervalInput.value;
            debrid.auto_expire_links_after = autoExpireLinksAfterInput.value;

            if (debrid.name && debrid.api_key && debrid.provider) {
                debrids.push(debrid);
            }
        });

        return debrids;
    }

    collectArrConfigs() {
        const arrs = [];

        this.refs.arrConfigs.querySelectorAll('.arr-config').forEach((arrCard) => {
            const index = arrCard.getAttribute('data-index');
            const getField = (field) => arrCard.querySelector(`[name="arr[${index}].${field}"]`);

            const nameInput = getField('name');
            const hostInput = getField('host');
            const tokenInput = getField('token');
            const cleanupInput = getField('cleanup');
            const skipRepairInput = getField('skip_repair');
            const downloadUncachedInput = getField('download_uncached');
            const selectedDebridInput = getField('selected_debrid');
            const sourceInput = getField('source');

            if (!nameInput || !hostInput || !tokenInput || !cleanupInput || !skipRepairInput || !downloadUncachedInput || !selectedDebridInput || !sourceInput) {
                return;
            }

            const arr = {
                name: nameInput.value,
                host: hostInput.value,
                token: tokenInput.value,
                cleanup: cleanupInput.checked,
                skip_repair: skipRepairInput.checked,
                download_uncached: downloadUncachedInput.checked,
                selected_debrid: selectedDebridInput.value,
                source: sourceInput.value
            };

            if (arr.name && arr.host) {
                arrs.push(arr);
            }
        });

        return arrs;
    }

    collectMountConfig() {
        // Get the selected radio button value
        const selectedMountType = document.querySelector('input[name="mount.type"]:checked');

        return {
            type: selectedMountType ? selectedMountType.value : 'none',
            mount_path: document.querySelector('[name="mount.mount_path"]').value,
            dfs: this.collectDFSConfig(),
            rclone: this.collectRcloneConfig(),
            external_rclone: this.collectExternalRclone()
        };
    }

    collectExternalRclone() {
        return {
            rc_url: document.querySelector('[name="mount.external_rclone.rc_url"]')?.value || "",
            rc_username: document.querySelector('[name="mount.external_rclone.rc_username"]')?.value || "",
            rc_password: document.querySelector('[name="mount.external_rclone.rc_password"]')?.value || "",
        };
    }

    collectRcloneConfig() {
        const getElementValue = (name, defaultValue = '') => {
            const element = document.querySelector(`[name="mount.rclone.${name}"]`);
            if (!element) return defaultValue;

            if (element.type === 'checkbox') {
                return element.checked;
            } else if (element.type === 'number') {
                const val = parseInt(element.value);
                return isNaN(val) ? 0 : val;
            } else {
                return element.value || defaultValue;
            }
        };

        return {
            port: getElementValue('port', "5572"),
            buffer_size: getElementValue('buffer_size'),
            bw_limit: getElementValue('bw_limit'),
            cache_dir: getElementValue('cache_dir'),
            transfers: getElementValue('transfers', 8),
            vfs_cache_mode: getElementValue('vfs_cache_mode', 'off'),
            vfs_cache_max_age: getElementValue('vfs_cache_max_age', '1h'),
            vfs_cache_max_size: getElementValue('vfs_cache_max_size'),
            vfs_cache_poll_interval: getElementValue('vfs_cache_poll_interval', '1m'),
            vfs_read_chunk_size: getElementValue('vfs_read_chunk_size', ''),
            vfs_read_chunk_size_limit: getElementValue('vfs_read_chunk_size_limit', 'off'),
            vfs_cache_min_free_space: getElementValue('vfs_cache_min_free_space', ''),
            vfs_fast_fingerprint: getElementValue('vfs_fast_fingerprint', false),
            vfs_read_chunk_streams: getElementValue('vfs_read_chunk_streams', 0),
            use_mmap: getElementValue('use_mmap', false),
            async_read: getElementValue('async_read', true),
            uid: getElementValue('uid', 0),
            gid: getElementValue('gid', 0),
            umask: getElementValue('umask', ''),
            vfs_read_ahead: getElementValue('vfs_read_ahead', ''),
            attr_timeout: getElementValue('attr_timeout', '1s'),
            dir_cache_time: getElementValue('dir_cache_time', '5m'),
            no_modtime: getElementValue('no_modtime', false),
            no_checksum: getElementValue('no_checksum', false),
            log_level: getElementValue('log_level', 'INFO'),
        };
    }

    collectDFSConfig() {
        const getElementValue = (name, defaultValue = '') => {
            const element = document.querySelector(`[name="mount.dfs.${name}"]`);
            if (!element) return defaultValue;

            if (element.type === 'checkbox') {
                return element.checked;
            } else if (element.type === 'number') {
                const val = parseInt(element.value);
                return isNaN(val) ? 0 : val;
            } else {
                return element.value || defaultValue;
            }
        };

        return {
            cache_dir: getElementValue('cache_dir'),
            disk_cache_size: getElementValue('disk_cache_size'),
            cache_expiry: getElementValue('cache_expiry'),
            cache_cleanup_interval: getElementValue('cache_cleanup_interval'),
            chunk_size: getElementValue('chunk_size'),
            read_ahead_size: getElementValue('read_ahead_size'),
            daemon_timeout: getElementValue('daemon_timeout'),
            uid: getElementValue('uid', 0),
            gid: getElementValue('gid', 0),
            umask: getElementValue('umask'),
        };
    }

    setupMagnetHandler() {
        window.registerMagnetLinkHandler = () => {
            if ('registerProtocolHandler' in navigator) {
                try {
                    navigator.registerProtocolHandler(
                        'magnet',
                        `${window.location.origin}${window.urlBase}download?magnet=%s`,
                        'Decypharr'
                    );
                    localStorage.setItem('magnetHandler', 'true');
                    const btn = document.getElementById('registerMagnetLink');
                    btn.innerHTML = '<i class="bi bi-check-circle mr-2"></i>Magnet Handler Registered';
                    btn.classList.remove('btn-primary');
                    btn.classList.add('btn-success');
                    btn.disabled = true;
                    window.decypharrUtils.createToast('Magnet link handler registered successfully');
                } catch (error) {
                    console.error('Failed to register magnet link handler:', error);
                    window.decypharrUtils.createToast('Failed to register magnet link handler', 'error');
                }
            } else {
                window.decypharrUtils.createToast('Magnet link registration not supported in this browser', 'warning');
            }
        };

        // Check if already registered
        if (localStorage.getItem('magnetHandler') === 'true') {
            const btn = document.getElementById('registerMagnetLink');
            if (btn) {
                btn.innerHTML = '<i class="bi bi-check-circle mr-2"></i>Magnet Handler Registered';
                btn.classList.remove('btn-primary');
                btn.classList.add('btn-success');
                btn.disabled = true;
            }
        }
    }

    populateAPIToken(config) {
        const tokenDisplay = document.getElementById('api-token-display');
        if (tokenDisplay) {
            tokenDisplay.value = config.api_token || '****';
        }

        // Populate username (password is not populated for security)
        const usernameField = document.getElementById('auth-username');
        if (usernameField && config.auth_username) {
            usernameField.value = config.auth_username;
        }
    }

    // Virtual Folders Management
    populateVirtualFolders(customFolders) {
        if (!customFolders) return;

        Object.entries(customFolders).forEach(([folderName, folderData]) => {
            this.addVirtualFolder(folderName, folderData.filters);
        });
    }

    addVirtualFolder(folderName = '', filters = {}) {
        const id = this.virtualFolderCount++;
        const filterEntries = Object.entries(filters);

        const folderHtml = `
            <div class="card bg-base-200 shadow-sm" data-virtual-folder="${id}">
                <div class="card-body p-4">
                    <div class="flex justify-between items-start mb-4">
                        <h4 class="font-semibold text-lg">Virtual Folder</h4>
                        <button type="button" class="btn btn-ghost btn-sm btn-circle" onclick="configManager.removeVirtualFolder(${id});">
                            <i class="bi bi-x-lg"></i>
                        </button>
                    </div>

                    <div class="space-y-4">
                        <div>
                            <label class="label">
                                <span class="font-medium">Folder Name</span>
                            </label>
                            <input type="text"
                                   class="input input-bordered w-full"
                                   name="virtual_folder_${id}_name"
                                   value="${window.decypharrUtils.escapeHtml(folderName)}"
                                   placeholder="e.g., Movies, TV Shows, 4K"
                                   required>
                            <span class="text-sm opacity-70">This folder will appear in your mount</span>
                        </div>

                        <div>
                            <label class="label">
                                <span class="font-medium">Filters</span>
                                <button type="button" class="btn btn-xs btn-primary" onclick="configManager.addVirtualFolderFilter(${id});">
                                    <i class="bi bi-plus"></i> Add Filter
                                </button>
                            </label>
                            <div class="space-y-2" id="virtual_folder_${id}_filters">
                                ${filterEntries.length > 0 ? filterEntries.map(([key, value], index) => `
                                    <div class="flex gap-2" data-filter-index="${index}">
                                        <input type="text"
                                               class="input input-bordered input-sm flex-1"
                                               name="virtual_folder_${id}_filter_key_${index}"
                                               value="${window.decypharrUtils.escapeHtml(key)}"
                                               placeholder="Filter key (e.g., name, category)">
                                        <input type="text"
                                               class="input input-bordered input-sm flex-1"
                                               name="virtual_folder_${id}_filter_value_${index}"
                                               value="${window.decypharrUtils.escapeHtml(value)}"
                                               placeholder="Filter value (e.g., *movie*, tv)">
                                        <button type="button" class="btn btn-sm btn-ghost btn-circle" onclick="configManager.removeVirtualFolderFilter(${id}, ${index});">
                                            <i class="bi bi-trash"></i>
                                        </button>
                                    </div>
                                `).join('') : `
                                    <div class="text-sm opacity-70">No filters. Click "Add Filter" to add one.</div>
                                `}
                            </div>
                            <span class="text-sm opacity-70">Filters use wildcards: * for any characters, ? for single character</span>
                        </div>
                    </div>
                </div>
            </div>
        `;

        this.refs.virtualFoldersContainer.insertAdjacentHTML('beforeend', folderHtml);
    }

    addVirtualFolderFilter(folderId) {
        const container = document.getElementById(`virtual_folder_${folderId}_filters`);
        const existingFilters = container.querySelectorAll('[data-filter-index]');
        const newIndex = existingFilters.length;

        // Remove "no filters" message if it exists
        const noFiltersMsg = container.querySelector('.text-sm.opacity-70');
        if (noFiltersMsg) {
            noFiltersMsg.remove();
        }

        const filterHtml = `
            <div class="flex gap-2" data-filter-index="${newIndex}">
                <input type="text"
                       class="input input-bordered input-sm flex-1"
                       name="virtual_folder_${folderId}_filter_key_${newIndex}"
                       placeholder="Filter key (e.g., name, category)">
                <input type="text"
                       class="input input-bordered input-sm flex-1"
                       name="virtual_folder_${folderId}_filter_value_${newIndex}"
                       placeholder="Filter value (e.g., *movie*, tv)">
                <button type="button" class="btn btn-sm btn-ghost btn-circle" onclick="configManager.removeVirtualFolderFilter(${folderId}, ${newIndex});">
                    <i class="bi bi-trash"></i>
                </button>
            </div>
        `;

        container.insertAdjacentHTML('beforeend', filterHtml);
    }

    removeVirtualFolderFilter(folderId, filterIndex) {
        const container = document.getElementById(`virtual_folder_${folderId}_filters`);
        const filter = container.querySelector(`[data-filter-index="${filterIndex}"]`);
        if (filter) {
            filter.remove();
        }

        // Show "no filters" message if no filters remain
        const remainingFilters = container.querySelectorAll('[data-filter-index]');
        if (remainingFilters.length === 0) {
            container.innerHTML = '<div class="text-sm opacity-70">No filters. Click "Add Filter" to add one.</div>';
        }
    }

    removeVirtualFolder(id) {
        const folder = document.querySelector(`[data-virtual-folder="${id}"]`);
        if (folder && confirm('Are you sure you want to remove this virtual folder?')) {
            folder.remove();
        }
    }

    collectVirtualFolders() {
        const customFolders = {};
        const folderElements = this.refs.virtualFoldersContainer.querySelectorAll('[data-virtual-folder]');

        folderElements.forEach(folderEl => {
            const id = folderEl.getAttribute('data-virtual-folder');
            const nameInput = folderEl.querySelector(`[name="virtual_folder_${id}_name"]`);
            const folderName = nameInput ? nameInput.value.trim() : '';

            if (folderName) {
                const filters = {};
                const filterContainer = document.getElementById(`virtual_folder_${id}_filters`);
                const filterElements = filterContainer.querySelectorAll('[data-filter-index]');

                filterElements.forEach(filterEl => {
                    const index = filterEl.getAttribute('data-filter-index');
                    const keyInput = filterEl.querySelector(`[name="virtual_folder_${id}_filter_key_${index}"]`);
                    const valueInput = filterEl.querySelector(`[name="virtual_folder_${id}_filter_value_${index}"]`);

                    const key = keyInput ? keyInput.value.trim() : '';
                    const value = valueInput ? valueInput.value.trim() : '';

                    if (key && value) {
                        filters[key] = value;
                    }
                });

                customFolders[folderName] = {filters};
            }
        });

        return customFolders;
    }

    // Usenet Configuration Methods
    populateUsenetSettings(usenet) {
        // Populate providers
        if (usenet.providers && Array.isArray(usenet.providers)) {
            usenet.providers.forEach(provider => this.addUsenetProvider(provider));
        }

        // Populate stream settings
        const streamFields = {
            'max_connections': usenet.max_connections,
            'read_ahead': usenet.read_ahead,
            'processing_timeout': usenet.processing_timeout,
            'availability_sample_percent': usenet.availability_sample_percent,
            'max_concurrent_nzb': usenet.max_concurrent_nzb,
            'disk_buffer_path': usenet.disk_buffer_path,
            'skip_repair': usenet.skip_repair
        };

        Object.entries(streamFields).forEach(([id, value]) => {
            const input = document.getElementsByName(`usenet.${id}`)[0];
            if (input && value !== undefined) {
                input.value = value;
            }
        });
    }

    addUsenetProvider(data = {}) {
        const providerHtml = this.getUsenetProviderTemplate(this.usenetProviderCount, data);
        this.refs.usenetProviders.insertAdjacentHTML('beforeend', providerHtml);

        // Populate data if provided
        if (Object.keys(data).length > 0) {
            this.populateUsenetProviderData(this.usenetProviderCount, data);
        }

        this.usenetProviderCount++;
    }

    populateUsenetProviderData(index, data) {
        Object.entries(data).forEach(([key, value]) => {
            const input = document.querySelector(`[name="usenet.providers[${index}].${key}"]`);
            if (input) {
                if (input.type === 'checkbox') {
                    input.checked = value;
                } else {
                    input.value = value;
                }
            }
        });
    }

    getUsenetProviderTemplate(index, data = {}) {
        return `
        <div class="card bg-base-200 border border-base-300 usenet-provider" data-index="${index}">
            <div class="card-body">
                <div class="flex justify-between items-start mb-4">
                    <h4 class="font-bold text-lg">
                        <i class="bi bi-server mr-2"></i>
                        Provider #${index + 1}
                    </h4>
                    <button type="button" class="btn btn-error btn-sm" onclick="this.closest('.usenet-provider').remove();">
                        <i class="bi bi-trash"></i>
                    </button>
                </div>

                <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
                    <div>
                        <label class="label" for="usenet_provider_${index}_host">
                            <span class="font-medium">Server Host</span>
                        </label>
                        <input type="text" class="input w-full"
                               name="usenet.providers[${index}].host"
                               id="usenet_provider_${index}_host"
                               placeholder="news.usenetexpress.com"
                               required>
                        <span class="text-sm opacity-70">NNTP server hostname</span>
                    </div>
                    <div>
                        <label class="label" for="usenet_provider_${index}_username">
                            <span class="font-medium">Username</span>
                        </label>
                        <input type="text" class="input w-full"
                               name="usenet.providers[${index}].username"
                               id="usenet_provider_${index}_username"
                               autocomplete="off">
                        <span class="text-sm opacity-70">NNTP username</span>
                    </div>
                    <div>
                        <label class="label" for="usenet_provider_${index}_password">
                            <span class="font-medium">Password</span>
                        </label>
                        <div class="password-toggle-container">
                            <input type="password" class="input input-has-toggle"
                                   name="usenet.providers[${index}].password"
                                   id="usenet_provider_${index}_password"
                                   autocomplete="new-password">
                            <button type="button" class="password-toggle-btn">
                                <i class="bi bi-eye" id="usenet_provider_${index}_password_icon"></i>
                            </button>
                        </div>
                        <span class="text-sm opacity-70">NNTP password</span>
                    </div>

                    <div>
                        <label class="label" for="usenet_provider_${index}_port">
                            <span class="font-medium">Port</span>
                        </label>
                        <input type="number" class="input w-full"
                               name="usenet.providers[${index}].port"
                               id="usenet_provider_${index}_port"
                               placeholder="119"
                               min="1" max="65535"
                               value="119">
                        <span class="text-sm opacity-70">NNTP port (563 for SSL, 119 for plain)</span>
                    </div>
                    <div>
                        <label class="label" for="usenet_provider_${index}_backbone">
                            <span class="font-medium">Backbone</span>
                        </label>
                        <input type="text" class="input w-full"
                               name="usenet.providers[${index}].backbone"
                               id="usenet_provider_${index}_backbone"
                               placeholder="Omicron">
                        <span class="text-sm opacity-70">Optional shared article backbone for smarter 430 failover</span>
                    </div>

                    <div>
                        <label class="label" for="usenet_provider_${index}_max_connections">
                            <span class="font-medium">Max Connections</span>
                        </label>
                        <input type="number" class="input w-full"
                               name="usenet.providers[${index}].max_connections"
                               id="usenet_provider_${index}_max_connections">
                        <span class="text-sm opacity-70">Max connections for this provider</span>
                    </div>
                    <div>
                        <label class="label" for="usenet_provider_${index}_priority">
                            <span class="font-medium">Priority</span>
                        </label>
                        <input type="number" class="input w-full"
                               name="usenet.providers[${index}].priority"
                               id="usenet_provider_${index}_priority">
                        <span class="text-sm opacity-70">Priority for this provider (lower number = higher priority)</span>
                    </div>
                </div>

                <div class="flex gap-4 mt-4">
                    <label class="flex items-center gap-2 cursor-pointer">
                        <input type="checkbox" class="checkbox checkbox-primary checkbox-sm"
                               name="usenet.providers[${index}].ssl"
                               id="usenet_provider_${index}_ssl">
                        <span class="text-sm">Use SSL</span>
                    </label>
                </div>
            </div>
        </div>
        `;
    }
}
