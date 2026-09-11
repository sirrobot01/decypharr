// Configuration management for Decypharr
class ConfigManager {
    constructor() {
        this.debridCount = 0;
        this.arrCount = 0;
        this.usenetProviderCount = 0;
        this.debridDirectoryCounts = {};
        this.directoryFilterCounts = {};
        this.virtualFolderCount = 0;
        this.virtualFolderConditionCounts = {};
        this.virtualFolderProviders = [];

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

		this.refs.virtualFoldersContainer.addEventListener('input', (event) => this.handleVirtualFolderInput(event));
		this.refs.virtualFoldersContainer.addEventListener('change', (event) => this.handleVirtualFolderInput(event));

        const addRuleBtn = document.getElementById('addQueueCleanupRuleBtn');
        if (addRuleBtn) addRuleBtn.addEventListener('click', () => this.addQueueCleanupCustomRow());

        const strmRegenerateBtn = document.getElementById('strmRegenerateBtn');
        if (strmRegenerateBtn) strmRegenerateBtn.addEventListener('click', () => this.regenerateStrm());
    }

    // Display labels for the built-in queue-cleanup catalog. IDs MUST match
    // config.DefaultQueueCleanupRules / catalogMatchers on the backend.
    get queueCleanupCatalog() {
        return [
            {id: 'failed_download', label: 'Failed download'},
            {id: 'title_mismatch', label: 'Title mismatch (automatic import not possible)'},
            {id: 'matched_by_id', label: 'Release matched to series/movie by ID'},
            {id: 'unable_to_parse', label: 'Unable to parse download'},
            {id: 'no_eligible_files', label: 'No files eligible for import'},
            {id: 'episodes_missing', label: 'Episodes missing / not imported from release'},
            {id: 'file_empty', label: 'Downloaded file is empty'},
            {id: 'invalid_local_path', label: 'Invalid local path (remote path mapping)'},
            {id: 'not_grabbed', label: 'Not grabbed by the arr / no category'},
        ];
    }

    queueCleanupActionOptions(selected) {
        const opts = [
            {value: '', label: 'Ignore (leave in queue)'},
            {value: 'import', label: 'Force import'},
            {value: 'blacklist', label: 'Blacklist only'},
            {value: 'blacklist_research', label: 'Blacklist + research'},
        ];
        return opts.map(o =>
            `<option value="${o.value}" ${o.value === (selected || '') ? 'selected' : ''}>${o.label}</option>`
        ).join('');
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
        this.populateDownloadSettings(config);
        this.populateStrmSettings(config.strm);

        // Load debrid configs
        if (config.debrids && Array.isArray(config.debrids)) {
            config.debrids.forEach(debrid => this.addDebridConfig(debrid));
        }

        // Load usenet config
        if (config.usenet) {
            this.populateUsenetSettings(config.usenet);
        }

        // Load virtual folders after provider names are known so the rule
        // builder can offer provider choices.
        this.virtualFolderProviders = (config.debrids || []).map(debrid => debrid.name).filter(Boolean);
        if (config.usenet?.providers?.length) this.virtualFolderProviders.push('usenet');
        this.populateVirtualFolders(config.virtual_folders || []);

        // Load Arr configs
        if (config.arrs && Array.isArray(config.arrs)) {
            config.arrs.forEach(arr => this.addArrConfig(arr));
        }

        // Load queue cleanup rules
        this.populateQueueCleanup(config.queue_cleanup);

        // Load rclone config
        this.populateMountSettings(config.mount);
        this.populateNFSSettings(config.nfs);
        this.populateSMBSettings(config.smb);
        this.populateShareCacheSettings(config.share_cache);

        // Load API token info
        this.populateAPIToken(config);

        // Load notifications config
        this.populateNotificationSettings(config.notifications);

        // Load repair config
        this.populateRepairSettings(config.repair, config.arrs);

        // Load hearsay config
        this.populateHearsaySettings(config.hearsay);
    }

    populateHearsaySettings(hearsay) {
        const $ = (id) => document.getElementById(id);
        const h = hearsay || {};
        if ($('hearsay.enabled')) $('hearsay.enabled').checked = !h.disabled;
        if ($('hearsay.participate')) $('hearsay.participate').checked = h.participate ?? true;
        if ($('hearsay.publish')) $('hearsay.publish').checked = h.publish ?? true;
        if ($('hearsay.advice_mode')) $('hearsay.advice_mode').value = h.advice_mode || 'shadow';
        if ($('hearsay.min_support')) $('hearsay.min_support').value = h.min_support || '';
        if ($('hearsay.min_evidence')) $('hearsay.min_evidence').value = h.min_evidence || '';
        if ($('hearsay.min_sources')) $('hearsay.min_sources').value = h.min_sources || '';
        if ($('hearsay.port')) $('hearsay.port').value = h.port || '';
        if ($('hearsay.gossip_port')) $('hearsay.gossip_port').value = h.gossip_port || '';
        if ($('hearsay.interval')) $('hearsay.interval').value = h.interval || '';
        if ($('hearsay.max_storage_bytes')) $('hearsay.max_storage_bytes').value = h.max_storage_bytes || '';
        if ($('hearsay.max_feeds_per_namespace')) $('hearsay.max_feeds_per_namespace').value = h.max_feeds_per_namespace || '';
        if ($('hearsay.follow')) $('hearsay.follow').value = (h.follow || []).join('\n');
    }

    collectHearsayConfig() {
        const $ = (id) => document.getElementById(id);
        return {
            disabled: !($('hearsay.enabled')?.checked ?? true),
            participate: $('hearsay.participate')?.checked ?? true,
            publish: $('hearsay.publish')?.checked ?? true,
            advice_mode: $('hearsay.advice_mode')?.value || 'shadow',
            min_support: parseFloat($('hearsay.min_support')?.value) || 0,
            min_evidence: parseFloat($('hearsay.min_evidence')?.value) || 0,
            min_sources: parseInt($('hearsay.min_sources')?.value, 10) || 0,
            port: parseInt($('hearsay.port')?.value, 10) || 0,
            gossip_port: parseInt($('hearsay.gossip_port')?.value, 10) || 0,
            interval: $('hearsay.interval')?.value.trim() || '',
            max_storage_bytes: parseInt($('hearsay.max_storage_bytes')?.value, 10) || 0,
            max_feeds_per_namespace: parseInt($('hearsay.max_feeds_per_namespace')?.value, 10) || 0,
            follow: ($('hearsay.follow')?.value || '')
                .split('\n').map((k) => k.trim()).filter(Boolean),
        };
    }

    populateRepairSettings(repair, arrs) {
        // Always refresh the Arr choices so they track the latest *Arrs config.
        const arrsGroup = document.getElementById('repair.arrs');
        if (arrsGroup) {
            const wanted = new Set((repair && Array.isArray(repair.arrs)) ? repair.arrs : []);
            arrsGroup.innerHTML = '';
            let choiceCount = 0;
            for (const a of (arrs || [])) {
                if (!a || !a.name) continue;

                const label = document.createElement('label');
                label.className = 'flex cursor-pointer items-center gap-3 rounded-lg px-3 py-2 hover:bg-base-200';

                const checkbox = document.createElement('input');
                checkbox.className = 'checkbox checkbox-primary checkbox-sm';
                checkbox.type = 'checkbox';
                checkbox.name = 'repair.arrs[]';
                checkbox.value = a.name;
                checkbox.checked = wanted.has(a.name);

                const name = document.createElement('span');
                name.className = 'min-w-0 truncate text-sm';
                name.textContent = a.name;

                label.appendChild(checkbox);
                label.appendChild(name);
                arrsGroup.appendChild(label);
                choiceCount++;
            }

            if (choiceCount === 0) {
                const empty = document.createElement('p');
                empty.className = 'px-3 py-2 text-sm opacity-60';
                empty.textContent = 'No eligible Arrs configured.';
                arrsGroup.appendChild(empty);
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
        if ($('repair.stop_schedule')) $('repair.stop_schedule').value = repair.stop_schedule || '';
        if ($('repair.auto_repair')) $('repair.auto_repair').checked = !!repair.auto_repair;
        if ($('repair.verify_content')) $('repair.verify_content').checked = !!repair.verify_content;
    }

    collectRepairConfig() {
        const $ = (id) => document.getElementById(id);
        const arrsGroup = $('repair.arrs');
        const arrs = arrsGroup
            ? Array.from(arrsGroup.querySelectorAll('input[type="checkbox"]:checked'))
                .map((choice) => choice.value)
                .filter(Boolean)
            : [];
        return {
            enabled: $('repair.enabled')?.checked || false,
            source: $('repair.source')?.value || 'arr',
            schedule: $('repair.schedule')?.value.trim() || '',
            recheck_interval: $('repair.recheck_interval')?.value.trim() || '',
            workers: parseInt($('repair.workers')?.value, 10) || 0,
            nntp_connection_percent: parseInt($('repair.nntp_connection_percent')?.value, 10) || 0,
            strategy: $('repair.strategy')?.value || 'per_entry',
            stop_schedule: $('repair.stop_schedule')?.value.trim() || '',
            auto_repair: $('repair.auto_repair')?.checked || false,
            verify_content: $('repair.verify_content')?.checked || false,
            arrs,
        };
    }

    populateGeneralSettings(config) {
        const fields = [
            'log_level', 'url_base', 'bind_address', 'port',
            'min_file_size', 'max_file_size', 'folder_naming',
            'refresh_dirs', 'disable_webdav', 'app_url'
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
    }

    populateDownloadSettings(config) {
        const fields = [
            'remove_stalled_after', 'nzb_user_agent', 'download_folder',
            'refresh_interval', 'max_active_downloads', 'skip_pre_cache',
            'always_rm_tracker_urls', 'default_download_action'
        ];

        fields.forEach(field => {
            const element = document.querySelector(`[name="${field}"]`);
            if (element && config[field] !== undefined) {
                if (element.type === 'checkbox') {
                    element.checked = config[field];
                } else {
                    element.value = config[field];
                }
            }
        });
    }

    populateStrmSettings(strmConfig) {
        if (!strmConfig) return;
        document.querySelector('[name="strm_enabled"]').checked = strmConfig.enabled || false;
        document.querySelector('[name="strm_path"]').value = strmConfig.path || '';
        document.querySelector('[name="strm_delivery_mode"]').value = strmConfig.delivery_mode || 'proxy';
        document.querySelector('[name="strm_sidecar_max_size"]').value = strmConfig.sidecar_max_size || '';
        document.querySelector('[name="strm_keep_media_extension"]').checked = strmConfig.keep_media_extension || false;
        document.querySelector('[name="strm_download_sidecars"]').checked = strmConfig.download_sidecars !== false;
    }

    collectStrmConfig() {
        return {
            enabled: document.querySelector('[name="strm_enabled"]')?.checked || false,
            path: document.querySelector('[name="strm_path"]')?.value.trim() || '',
            delivery_mode: document.querySelector('[name="strm_delivery_mode"]')?.value || 'proxy',
            sidecar_max_size: document.querySelector('[name="strm_sidecar_max_size"]')?.value.trim() || '',
            keep_media_extension: document.querySelector('[name="strm_keep_media_extension"]')?.checked || false,
            download_sidecars: document.querySelector('[name="strm_download_sidecars"]')?.checked ?? true,
        };
    }

    async regenerateStrm() {
        const btn = document.getElementById('strmRegenerateBtn');
        btn.disabled = true;
        try {
            const response = await window.decypharrUtils.fetcher('/api/strm/regenerate', {
                method: 'POST',
            });
            if (!response.ok) {
                throw new Error(await response.text());
            }
            window.decypharrUtils.createToast('STRM regeneration started in the background', 'success');
        } catch (error) {
            window.decypharrUtils.createToast(`STRM regenerate failed: ${error.message}`, 'error');
        } finally {
            btn.disabled = false;
        }
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

    populateNFSSettings(nfsConfig) {
        if (!nfsConfig) return;

        const enabled = document.querySelector('[name="nfs.enabled"]');
        if (enabled) enabled.checked = Boolean(nfsConfig.enabled);

        for (const field of ['bind_address', 'port']) {
            const element = document.querySelector(`[name="nfs.${field}"]`);
            if (element && nfsConfig[field] !== undefined) {
                element.value = nfsConfig[field];
            }
        }

        const networks = document.querySelector('[name="nfs.allowed_networks"]');
        if (networks && Array.isArray(nfsConfig.allowed_networks)) {
            networks.value = nfsConfig.allowed_networks.join('\n');
        }
    }

    populateSMBSettings(smbConfig) {
        if (!smbConfig) return;

        for (const field of ['enabled', 'require_signing']) {
            const element = document.querySelector(`[name="smb.${field}"]`);
            if (element) element.checked = Boolean(smbConfig[field]);
        }

        for (const field of ['bind_address', 'port', 'share_name', 'username', 'password']) {
            const element = document.querySelector(`[name="smb.${field}"]`);
            if (element && smbConfig[field] !== undefined) {
                element.value = smbConfig[field];
            }
        }

        const networks = document.querySelector('[name="smb.allowed_networks"]');
        if (networks && Array.isArray(smbConfig.allowed_networks)) {
            networks.value = smbConfig.allowed_networks.join('\n');
        }
    }

    populateShareCacheSettings(cacheConfig) {
        if (!cacheConfig) return;

        const enabled = document.querySelector('[name="share_cache.enabled"]');
        // Unset means disabled: the cache is opt-in.
        if (enabled) enabled.checked = cacheConfig.enabled === true;

        for (const field of ['dir', 'max_size', 'max_age', 'chunk_size', 'read_ahead']) {
            const element = document.querySelector(`[name="share_cache.${field}"]`);
            if (element && cacheConfig[field] !== undefined) {
                element.value = cacheConfig[field];
            }
        }
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
            'cache_dir', 'disk_cache_size', 'buffer_memory', 'cache_expiry', 'cache_cleanup_interval',
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
                <div class="mb-4 flex items-start justify-between gap-3">
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
                        <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
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
                <div class="mt-6 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
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
                    <div class="mb-4 flex items-start justify-between gap-3">
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
                if (key === 'download_uncached' && input.tagName === 'SELECT') {
                    input.value = value === true ? 'true' : value === false ? 'false' : '';
                } else if (input.type === 'checkbox') {
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
                <div class="card-body p-4 gap-4">
                    <div class="flex items-start justify-between gap-3">
                        <h3 class="card-title text-base leading-tight min-w-0">
                            <i class="bi bi-collection text-warning shrink-0"></i>
                            <span class="min-w-0 break-words">Arr Service #${index + 1}</span>
                            ${isAutoDetected ? '<span class="badge badge-info badge-sm shrink-0">Auto-detected</span>' : ''}
                        </h3>
                        ${!isAutoDetected ? `
                            <button type="button" class="btn btn-error btn-sm btn-square shrink-0" onclick="this.closest('.arr-config').remove();">
                                <i class="bi bi-trash"></i>
                            </button>
                        ` : ''}
                    </div>

                    <input type="hidden" name="arr[${index}].source" value="${data.source || ''}">

                    <div class="grid grid-cols-1 gap-3">
                        <div>
                            <label class="label" for="arr[${index}].name">
                                <span class="font-medium">Service Name</span>
                            </label>
                            <input type="text" class="input ${isAutoDetected ? 'input-disabled' : ''}"
                                   name="arr[${index}].name" id="arr[${index}].name"
                                   ${isAutoDetected ? 'readonly' : 'required'}
                                   placeholder="sonarr, radarr, etc.">
                        </div>

                        <div>
                            <label class="label" for="arr[${index}].host">
                                <span class="font-medium">Host URL</span>
                            </label>
                            <input type="url" class="input ${isAutoDetected ? 'input-disabled' : ''}"
                                   name="arr[${index}].host" id="arr[${index}].host"
                                   ${isAutoDetected ? 'readonly' : 'required'}
                                   placeholder="http://localhost:8989">
                        </div>

                        <div>
                            <label class="label" for="arr[${index}].token">
                                <span class="font-medium">API Token</span>
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
                                <span class="font-medium">Preferred Provider(debrid)</span>
                            </label>
                            <select class="select w-full" name="arr[${index}].selected_debrid" id="arr[${index}].selected_debrid">
                                <option value="">Auto Select</option>
                                ${debridOptions}
                            </select>
                            <span class="text-sm opacity-70">Which debrid service this Arr should prefer</span>
                        </div>
                    </div>

                    <div class="grid grid-cols-1 md:grid-cols-2 gap-2">
                        <div class="rounded-box bg-base-200/50 px-3 py-2">
                            <label class="label cursor-pointer justify-start gap-2 p-0">
                                <input type="checkbox" class="checkbox checkbox-sm checkbox-primary"
                                       name="arr[${index}].skip_repair" id="arr[${index}].skip_repair">
                                <span class="text-sm leading-tight">Skip Repair</span>
                            </label>
                        </div>

                        <div class="rounded-box bg-base-200/50 px-3 py-2">
                            <label class="label p-0 pb-1" for="arr[${index}].download_uncached">
                                <span class="text-sm leading-tight">Uncached Torrents</span>
                            </label>
                            <select class="select select-sm w-full"
                                    name="arr[${index}].download_uncached" id="arr[${index}].download_uncached">
                                <option value="">Inherit provider setting</option>
                                <option value="true">Allow for this Arr</option>
                                <option value="false">Require cached for this Arr</option>
                            </select>
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
                const virtualFolderErrors = validation.errors.filter(error => error.startsWith('Virtual folder'));
                const summary = document.getElementById('virtualFolderValidationSummary');
                if (summary && virtualFolderErrors.length) {
                    summary.querySelector('[data-vf-validation-list]').innerHTML = virtualFolderErrors
                        .map(error => `<li>${window.decypharrUtils.escapeHtml(error)}</li>`).join('');
                    summary.classList.remove('hidden');
                    window.location.hash = 'tab-virtual-folders';
                    setTimeout(() => summary.focus(), 0);
                }
                throw new Error(validation.errors.join('\n'));
            }

            document.getElementById('virtualFolderValidationSummary')?.classList.add('hidden');

            const response = await window.decypharrUtils.fetcher('/api/config', {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify(config)
            });

            if (!response.ok) {
                const errorText = await response.text();
                throw new Error(errorText || 'Failed to save configuration');
            }

            let restarted = true;
            try {
                const result = await response.json();
                restarted = result.restarted !== false;
            } catch (_) {
                // Older backends return no body; assume a restart happened.
            }

            if (restarted) {
                window.decypharrUtils.createToast('Configuration saved successfully! Services are restarting...', 'success');
                // Reload page after a delay to allow services to restart
                setTimeout(() => {
                    window.location.reload();
                }, 2000);
            } else {
                // Applied live — no restart, no disruptive reload.
                window.decypharrUtils.createToast('Configuration saved and applied.', 'success');
                this.refs.loadingOverlay.classList.add('hidden');
            }

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

        // Validate virtual folders using the same user-facing contract as the
        // backend. Case-insensitive checks avoid collisions on common mounts.
        const reservedFolderNames = new Set(['__all__', '__bad__', 'torrents', 'nzbs', 'version.txt']);
        config.debrids.forEach(debrid => reservedFolderNames.add((debrid.name || '').trim().toLowerCase()));
        const seenVirtualFolderNames = new Set();
        (config.virtual_folders || []).forEach((folder, index) => {
            this.validateVirtualFolderDefinition(folder).forEach(error => errors.push(`Virtual folder #${index + 1}: ${error}`));
            const normalizedName = (folder.name || '').trim().toLowerCase();
            if (!normalizedName) return;
            if (reservedFolderNames.has(normalizedName)) {
                errors.push(`Virtual folder "${folder.name}": the name conflicts with a built-in or provider folder.`);
            }
            if (seenVirtualFolderNames.has(normalizedName)) {
                errors.push(`Virtual folder "${folder.name}" is defined more than once.`);
            }
            seenVirtualFolderNames.add(normalizedName);
        });

        if (!config.mount.type) {
            errors.push('Mounts: a mount type must be selected');
        }

        // "none" runs a stub mount manager, so there is nothing to mount and no
        // path to configure. Every other type resolves entry paths under it.
        if (config.mount.type && config.mount.type !== 'none' && !config.mount.mount_path) {
            errors.push('Mounts: mount path is required unless the mount type is "No Mount"');
        }

        if (config.repair?.enabled && !config.repair.schedule) {
            errors.push('Repair: schedule is required when Repair is enabled');
        }

        // The SMB server grants no anonymous access and refuses to start
        // without credentials, so catch it here rather than at startup.
        if (config.smb?.enabled) {
            const missing = [];
            if (!config.smb.username) missing.push('username');
            if (!config.smb.password) missing.push('password');
            if (missing.length) {
                errors.push(`Shares > SMB: ${missing.join(' and ')} ${missing.length > 1 ? 'are' : 'is'} required when SMB is enabled`);
            }
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
            max_active_downloads: parseInt(document.querySelector('[name="max_active_downloads"]').value) || 5,
            skip_pre_cache: document.querySelector('[name="skip_pre_cache"]').checked,
            always_rm_tracker_urls: document.querySelector('[name="always_rm_tracker_urls"]').checked,
            folder_naming: document.querySelector('[name="folder_naming"]')?.value || "",
            disable_webdav: document.querySelector('[name="disable_webdav"]').checked,
            refresh_dirs: document.querySelector('[name="refresh_dirs"]')?.value || "",
            virtual_folders: this.collectVirtualFolders(),

            // Debrid configurations
            debrids: this.collectDebridConfigs(),

            // Arr configurations
            arrs: this.collectArrConfigs(),

            // Global arr queue-cleanup policy
            queue_cleanup: this.collectQueueCleanup(),

            // Mount configuration
            mount: this.collectMountConfig(),
            nfs: this.collectNFSConfig(),
            smb: this.collectSMBConfig(),
            share_cache: this.collectShareCacheConfig(),

            // Collect usenet configs
            usenet: this.collectUsenetConfig(),

            // Collect notifications config
            notifications: this.collectNotificationsConfig(),

            // Collect repair config
            repair: this.collectRepairConfig(),

            // Collect STRM config
            strm: this.collectStrmConfig(),

            // Collect hearsay config
            hearsay: this.collectHearsayConfig()
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
            const backupInput = getField('backup');

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
                priority: parseInt(priorityInput.value) || 0,
                // Backup is optional and defaults false — old form versions
                // (or missing inputs after a hot-reload) shouldn't accidentally
                // turn a primary into a backup.
                backup: backupInput ? backupInput.checked : false
            };

            if (provider.host && provider.username && provider.password) {
                providers.push(provider);
            }
        });

        return {
            providers: providers,
            max_connections: parseInt(document.querySelector('[name="usenet.max_connections"]')?.value) || 15,
            processing_max_connections: parseInt(document.querySelector('[name="usenet.processing_max_connections"]')?.value)
                || parseInt(document.querySelector('[name="usenet.max_connections"]')?.value)
                || 15,
            read_ahead: document.querySelector('[name="usenet.read_ahead"]').value || "16MB",
            processing_timeout: document.querySelector('[name="usenet.processing_timeout"]')?.value || "5m",
            conn_idle_timeout: document.querySelector('[name="usenet.conn_idle_timeout"]')?.value || "",
            availability_sample_percent: parseInt(document.querySelector('[name="usenet.availability_sample_percent"]')?.value) || 10,
            import_availability_sample_percent: parseInt(document.querySelector('[name="usenet.import_availability_sample_percent"]')?.value) || 1,
            disk_path: document.querySelector('[name="usenet.disk_path"]')?.value.trim() || "",
			buffer_memory: document.querySelector('[name="usenet.buffer_memory"]')?.value || ""
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
            const skipRepairInput = getField('skip_repair');
            const downloadUncachedInput = getField('download_uncached');
            const selectedDebridInput = getField('selected_debrid');
            const sourceInput = getField('source');

            if (!nameInput || !hostInput || !tokenInput || !skipRepairInput || !downloadUncachedInput || !selectedDebridInput || !sourceInput) {
                return;
            }

            const arr = {
                name: nameInput.value,
                host: hostInput.value,
                token: tokenInput.value,
                skip_repair: skipRepairInput.checked,
                selected_debrid: selectedDebridInput.value,
                source: sourceInput.value
            };

            if (downloadUncachedInput.value !== '') {
                arr.download_uncached = downloadUncachedInput.value === 'true';
            }

            if (arr.name && arr.host) {
                arrs.push(arr);
            }
        });

        return arrs;
    }

    populateQueueCleanup(queueCleanup) {
        const catalogEl = document.getElementById('queueCleanupCatalog');
        const customEl = document.getElementById('queueCleanupCustom');
        if (!catalogEl || !customEl) return;

        const rules = (queueCleanup && Array.isArray(queueCleanup.rules)) ? queueCleanup.rules : [];

        // Saved actions for catalog rules, keyed by id.
        const savedActions = {};
        const customRules = [];
        rules.forEach(r => {
            if (r && r.id) {
                savedActions[r.id] = r.action || '';
            } else if (r && (r.match || '').trim()) {
                customRules.push(r);
            }
        });

        const esc = window.decypharrUtils.escapeHtml;
        catalogEl.innerHTML = this.queueCleanupCatalog.map(c => {
            const action = c.id in savedActions ? savedActions[c.id] : '';
            return `
                <div class="grid grid-cols-1 md:grid-cols-5 gap-2 md:items-center px-3 py-3 even:bg-base-200/40" data-rule-id="${c.id}">
                    <span class="md:col-span-3 text-sm leading-snug min-w-0">${esc(c.label)}</span>
                    <select class="md:col-span-2 select select-sm w-full queue-cleanup-action">
                        ${this.queueCleanupActionOptions(action)}
                    </select>
                </div>`;
        }).join('');

        customEl.innerHTML = '';
        customRules.forEach(r => this.addQueueCleanupCustomRow(r.match, r.action));
    }

    addQueueCleanupCustomRow(match = '', action = '') {
        const customEl = document.getElementById('queueCleanupCustom');
        if (!customEl) return;
        const esc = window.decypharrUtils.escapeHtml;
        const row = document.createElement('div');
        row.className = 'grid grid-cols-1 md:grid-cols-12 gap-2 queue-cleanup-custom-row';
        row.innerHTML = `
            <input type="text" class="md:col-span-7 input input-sm w-full min-w-0 queue-cleanup-match"
                   placeholder="text in status message, e.g. stalled"
                   value="${esc(match)}">
            <select class="md:col-span-4 select select-sm w-full queue-cleanup-action">
                ${this.queueCleanupActionOptions(action)}
            </select>
            <button type="button" class="md:col-span-1 btn btn-sm btn-square btn-ghost text-error queue-cleanup-remove" aria-label="Remove custom queue cleanup rule" title="Remove rule">
                <i class="bi bi-trash"></i>
            </button>`;
        row.querySelector('.queue-cleanup-remove').addEventListener('click', () => row.remove());
        customEl.appendChild(row);
    }

    collectQueueCleanup() {
        const rules = [];

        document.querySelectorAll('#queueCleanupCatalog [data-rule-id]').forEach(rowEl => {
            const id = rowEl.getAttribute('data-rule-id');
            const action = rowEl.querySelector('.queue-cleanup-action')?.value || '';
            rules.push({id, action});
        });

        document.querySelectorAll('#queueCleanupCustom .queue-cleanup-custom-row').forEach(rowEl => {
            const match = (rowEl.querySelector('.queue-cleanup-match')?.value || '').trim();
            if (!match) return;
            const action = rowEl.querySelector('.queue-cleanup-action')?.value || '';
            rules.push({match, action});
        });

        return {rules};
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

    collectNFSConfig() {
        const value = (name, fallback = '') =>
            document.querySelector(`[name="nfs.${name}"]`)?.value || fallback;
        const port = Number.parseInt(value('port'), 10);
        const allowedNetworks = value('allowed_networks').split(/[\s,;]+/).filter(Boolean);

        return {
            enabled: document.querySelector('[name="nfs.enabled"]')?.checked || false,
            bind_address: value('bind_address', '0.0.0.0'),
            port: Number.isNaN(port) ? 20490 : port,
            allowed_networks: allowedNetworks
        };
    }

    collectSMBConfig() {
        const value = (name, fallback = '') =>
            document.querySelector(`[name="smb.${name}"]`)?.value || fallback;
        const checked = (name) => document.querySelector(`[name="smb.${name}"]`)?.checked || false;
        const port = Number.parseInt(value('port'), 10);
        const allowedNetworks = value('allowed_networks').split(/[\s,;]+/).filter(Boolean);

        return {
            enabled: checked('enabled'),
            bind_address: value('bind_address', '0.0.0.0'),
            port: Number.isNaN(port) ? 1445 : port,
            share_name: value('share_name', 'decypharr'),
            username: value('username'),
            password: value('password'),
            require_signing: checked('require_signing'),
            allowed_networks: allowedNetworks
        };
    }

    collectShareCacheConfig() {
        const value = (name) => document.querySelector(`[name="share_cache.${name}"]`)?.value?.trim() || '';

        return {
            enabled: document.querySelector('[name="share_cache.enabled"]')?.checked ?? false,
            dir: value('dir'),
            max_size: value('max_size'),
            max_age: value('max_age'),
            chunk_size: value('chunk_size'),
            read_ahead: value('read_ahead')
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
            buffer_memory: getElementValue('buffer_memory'),
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

        const tokenOnlyField = document.getElementById('auth-token-only');
        if (tokenOnlyField) {
            tokenOnlyField.checked = !!config.auth_token_only;
            if (typeof toggleTokenOnly === 'function') {
                toggleTokenOnly();
            }
        }
    }

    // Virtual Folders Management
    get virtualFolderFields() {
        const textOperators = [
            ['contains', 'contains'], ['not_contains', 'does not contain'],
            ['starts_with', 'starts with'], ['not_starts_with', 'does not start with'],
            ['ends_with', 'ends with'], ['not_ends_with', 'does not end with'],
            ['equals', 'is exactly'], ['not_equals', 'is not exactly'],
            ['matches_regex', 'matches regular expression'], ['not_matches_regex', 'does not match regular expression']
        ];
        return [
            {value: 'entry_name', label: 'Item name', help: 'The release or library folder name', operators: textOperators, placeholder: 'e.g., 2160p'},
            {value: 'file_name', label: 'File name inside item', help: 'Any file path contained in the item', operators: textOperators, placeholder: 'e.g., S01E'},
            {value: 'size', label: 'Total item size', help: 'The combined size of the item', operators: [['greater_than', 'is larger than'], ['less_than', 'is smaller than']], placeholder: 'e.g., 20GB'},
            {value: 'added', label: 'Date added', help: 'How recently the item was added', operators: [['within_last', 'is within the last']], placeholder: 'e.g., 7d'},
            {value: 'file_count', label: 'Number of files', help: 'How many files the item contains', operators: [['greater_than', 'is more than'], ['less_than', 'is fewer than']], placeholder: 'e.g., 5'},
            {value: 'protocol', label: 'Source type', help: 'Torrent or Usenet/NZB', operators: [['equals', 'is'], ['not_equals', 'is not']]},
            {value: 'provider', label: 'Provider', help: 'The provider currently serving the item', operators: [['equals', 'is'], ['not_equals', 'is not']], placeholder: 'Provider name'},
            {value: 'category', label: 'Category', help: 'The Arr or download category assigned to the item', operators: textOperators, placeholder: 'e.g., radarr'}
        ];
    }

    get virtualFolderSamples() {
        const episodePattern = '(?:S\\d{1,2}E\\d{1,3}|\\b\\d{1,2}x\\d{1,3}\\b)';
        return [
            {
                key: 'episodes',
                label: 'Episode files',
                description: 'Has a file named like S01E02 or 1x02',
                condition: {field: 'file_name', operator: 'matches_regex', value: episodePattern}
            },
            {
                key: 'movies',
                label: 'Likely movie',
                description: 'Has no file named like S01E02 or 1x02; this is a filename-based estimate',
                condition: {field: 'file_name', operator: 'not_matches_regex', value: episodePattern}
            },
            {
                key: 'multiple-files',
                label: 'More than one file',
                description: 'Contains at least two files',
                condition: {field: 'file_count', operator: 'greater_than', value: '1'}
            },
            {
                key: '4k',
                label: '4K items',
                description: 'Item name contains 2160p',
                condition: {field: 'entry_name', operator: 'contains', value: '2160p'}
            },
            {
                key: 'recent',
                label: 'Added this week',
                description: 'Was added within the last seven days',
                condition: {field: 'added', operator: 'within_last', value: '7d'}
            },
            {
                key: 'torrent',
                label: 'Torrent only',
                description: 'Uses a torrent source',
                condition: {field: 'protocol', operator: 'equals', value: 'torrent'}
            }
        ];
    }

    virtualFolderField(field) {
        return this.virtualFolderFields.find(option => option.value === field) || this.virtualFolderFields[0];
    }

    populateVirtualFolders(folders) {
        if (!Array.isArray(folders)) return;
        if (folders.length === 0) {
            this.renderVirtualFolderEmptyState();
            return;
        }
        folders.forEach(folder => this.addVirtualFolder(folder, false));
    }

    virtualFolderFieldOptions(selected) {
        return this.virtualFolderFields.map(field =>
            `<option value="${field.value}" ${field.value === selected ? 'selected' : ''}>${field.label}</option>`
        ).join('');
    }

    virtualFolderOperatorOptions(fieldName, selected) {
        const field = this.virtualFolderField(fieldName);
        return field.operators.map(([value, label]) =>
            `<option value="${value}" ${value === selected ? 'selected' : ''}>${label}</option>`
        ).join('');
    }

    virtualFolderValueControl(folderId, conditionId, condition) {
        const esc = window.decypharrUtils.escapeHtml;
        const id = `virtual_folder_${folderId}_condition_${conditionId}_value`;
        if (condition.field === 'protocol') {
            const value = ['torrent', 'nzb'].includes(condition.value) ? condition.value : 'torrent';
            return `<select id="${id}" class="select select-bordered w-full vf-condition-value" data-vf-value aria-required="true">
                <option value="torrent" ${value === 'torrent' ? 'selected' : ''}>Torrent</option>
                <option value="nzb" ${value === 'nzb' ? 'selected' : ''}>Usenet / NZB</option>
            </select>`;
        }
        if (condition.field === 'provider' && this.virtualFolderProviders.length > 0) {
            const values = [...this.virtualFolderProviders];
            if (condition.value && !values.includes(condition.value)) values.push(condition.value);
            return `<select id="${id}" class="select select-bordered w-full vf-condition-value" data-vf-value aria-required="true">
                ${values.map(value => `<option value="${esc(value)}" ${value === condition.value ? 'selected' : ''}>${esc(value)}</option>`).join('')}
            </select>`;
        }
        const field = this.virtualFolderField(condition.field);
        const numberAttributes = condition.field === 'file_count' ? 'type="number" min="0" step="1" inputmode="numeric"' : 'type="text"';
        return `<input ${numberAttributes} id="${id}" class="input input-bordered w-full vf-condition-value" data-vf-value aria-required="true"
            value="${esc(condition.value || '')}" placeholder="${esc(field.placeholder || '')}" autocomplete="off">`;
    }

    renderVirtualFolderCondition(folderId, conditionId, condition = {}) {
        const field = this.virtualFolderField(condition.field || 'entry_name');
        const operatorValues = field.operators.map(([value]) => value);
        const normalized = {
            field: field.value,
            operator: operatorValues.includes(condition.operator) ? condition.operator : operatorValues[0],
            value: condition.value || '',
            case_sensitive: Boolean(condition.case_sensitive)
        };
        const supportsCase = ['entry_name', 'file_name', 'provider', 'category'].includes(normalized.field);
        const fieldHelpId = `virtual_folder_${folderId}_condition_${conditionId}_help`;
        return `
            <div class="rounded-box border border-base-300 bg-base-100 p-4" data-vf-condition="${conditionId}" data-vf-current-field="${normalized.field}">
                <div class="grid grid-cols-1 gap-3 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)_auto] lg:items-end">
                    <div>
                        <label class="label py-1" for="virtual_folder_${folderId}_condition_${conditionId}_field"><span class="label-text font-medium">What to check</span></label>
                        <select id="virtual_folder_${folderId}_condition_${conditionId}_field" class="select select-bordered w-full" data-vf-field aria-describedby="${fieldHelpId}">
                            ${this.virtualFolderFieldOptions(normalized.field)}
                        </select>
                    </div>
                    <div>
                        <label class="label py-1" for="virtual_folder_${folderId}_condition_${conditionId}_operator"><span class="label-text font-medium">Rule</span></label>
                        <select id="virtual_folder_${folderId}_condition_${conditionId}_operator" class="select select-bordered w-full" data-vf-operator>
                            ${this.virtualFolderOperatorOptions(normalized.field, normalized.operator)}
                        </select>
                    </div>
                    <div>
                        <label class="label py-1" for="virtual_folder_${folderId}_condition_${conditionId}_value"><span class="label-text font-medium">Value</span></label>
                        ${this.virtualFolderValueControl(folderId, conditionId, normalized)}
                    </div>
                    <button type="button" class="btn btn-ghost btn-square text-error" aria-label="Remove this condition"
                            title="Remove condition" onclick="configManager.removeVirtualFolderCondition(${folderId}, ${conditionId})">
                        <i class="bi bi-trash" aria-hidden="true"></i>
                    </button>
                </div>
                <div class="mt-2 flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
                    <p id="${fieldHelpId}" class="text-xs text-base-content/65" data-vf-field-help>${field.help}</p>
                    <label class="label cursor-pointer justify-start gap-2 p-0 text-xs ${supportsCase ? '' : 'hidden'}" data-vf-case-wrap>
                        <input type="checkbox" class="checkbox checkbox-xs" data-vf-case ${normalized.case_sensitive ? 'checked' : ''}>
                        Match capitalization exactly
                    </label>
                </div>
            </div>`;
    }

    renderVirtualFolderSamples(folderId) {
        const esc = window.decypharrUtils.escapeHtml;
        const labelId = `virtual_folder_${folderId}_samples_label`;
        const helpId = `virtual_folder_${folderId}_samples_help`;
        return `<div class="rounded-box bg-base-200/70 p-4">
            <p id="${labelId}" class="text-sm font-semibold">Quick examples</p>
            <p id="${helpId}" class="mt-1 text-xs text-base-content/65">Choose one to fill an unused condition or add a new editable condition.</p>
            <div class="mt-3 flex flex-wrap gap-2" role="group" aria-labelledby="${labelId}" aria-describedby="${helpId}">
                ${this.virtualFolderSamples.map(sample => `<button type="button" class="btn btn-sm btn-outline bg-base-100"
                    title="${esc(sample.description)}" aria-label="${esc(`${sample.label}: ${sample.description}`)}"
                    onclick="configManager.applyVirtualFolderSample(${folderId}, '${sample.key}')">${esc(sample.label)}</button>`).join('')}
            </div>
        </div>`;
    }

    addVirtualFolder(definition = null, shouldFocus = true) {
        this.refs.virtualFoldersContainer.querySelector('[data-vf-empty-state]')?.remove();
        const isNewFolder = definition === null;
        definition = definition || {};
        const id = this.virtualFolderCount++;
        const conditions = Array.isArray(definition.conditions)
            ? definition.conditions
            : (isNewFolder ? [{field: 'entry_name', operator: 'contains', value: ''}] : []);
        this.virtualFolderConditionCounts[id] = conditions.length;
        const esc = window.decypharrUtils.escapeHtml;
        const name = definition.name || '';
        const title = name || `New virtual folder ${id + 1}`;
        const folderHtml = `
            <article class="card bg-base-100 shadow-sm border border-base-300" data-virtual-folder="${id}" aria-labelledby="virtual_folder_${id}_title">
                <div class="card-body p-4 sm:p-6 gap-5">
                    <div class="flex items-start justify-between gap-4">
                        <div>
                            <p class="text-xs font-semibold uppercase tracking-wide text-info">Filtered library view</p>
                            <h4 id="virtual_folder_${id}_title" class="mt-1 text-lg font-semibold" data-vf-title>${esc(title)}</h4>
                        </div>
                        <button type="button" class="btn btn-ghost btn-sm btn-square" aria-label="Remove virtual folder ${esc(title)}"
                                title="Remove virtual folder" onclick="configManager.removeVirtualFolder(${id})">
                            <i class="bi bi-x-lg" aria-hidden="true"></i>
                        </button>
                    </div>

                    <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
                        <div>
                            <label class="label" for="virtual_folder_${id}_name"><span class="label-text font-medium">Folder name</span></label>
                            <input type="text" id="virtual_folder_${id}_name" class="input input-bordered w-full" data-vf-name
                                   value="${esc(name)}" placeholder="e.g., 4K Movies" aria-describedby="virtual_folder_${id}_name_help" aria-required="true" autocomplete="off">
                            <p id="virtual_folder_${id}_name_help" class="mt-1 text-xs text-base-content/65">Appears at the top level of Browse, mounts, and shares.</p>
                        </div>
                        <div>
                            <label class="label" for="virtual_folder_${id}_match"><span class="label-text font-medium">An item should match</span></label>
                            <select id="virtual_folder_${id}_match" class="select select-bordered w-full" data-vf-match>
                                <option value="all" ${(definition.match || 'all') === 'all' ? 'selected' : ''}>All conditions</option>
                                <option value="any" ${definition.match === 'any' ? 'selected' : ''}>Any condition</option>
                            </select>
                            <p class="mt-1 text-xs text-base-content/65">This choice is explicit—there are no hidden combinations.</p>
                        </div>
                    </div>

                    <label class="label cursor-pointer justify-start gap-3 rounded-box bg-base-200 px-4 py-3">
                        <input type="checkbox" class="checkbox checkbox-sm checkbox-info" data-vf-include-bad ${definition.include_bad ? 'checked' : ''}>
                        <span><span class="font-medium">Include unhealthy items</span><span class="block text-xs text-base-content/65">Off by default so broken or unavailable entries stay out of this view.</span></span>
                    </label>

                    <fieldset class="space-y-3">
                        <legend class="sr-only">Conditions for ${esc(title)}</legend>
                        ${this.renderVirtualFolderSamples(id)}
                        <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                            <div>
                                <p class="font-semibold" aria-hidden="true">Conditions</p>
                                <p class="text-xs text-base-content/65">Text matching ignores capitalization unless you opt in per condition.</p>
                            </div>
                            <button type="button" class="btn btn-sm btn-outline btn-info" onclick="configManager.addVirtualFolderCondition(${id})">
                                <i class="bi bi-plus-lg" aria-hidden="true"></i> Add condition
                            </button>
                        </div>
                        <div class="space-y-3" id="virtual_folder_${id}_conditions" data-vf-conditions>
                            ${conditions.length ? conditions.map((condition, index) => this.renderVirtualFolderCondition(id, index, condition)).join('') : this.virtualFolderNoConditions()}
                        </div>
                    </fieldset>

                    <div class="flex flex-col gap-3 border-t border-base-300 pt-4 sm:flex-row sm:items-center sm:justify-between">
                        <p class="text-xs text-base-content/65"><i class="bi bi-shield-check mr-1" aria-hidden="true"></i>Removing this folder removes only the view, never the media.</p>
                        <button type="button" class="btn btn-sm btn-secondary" data-vf-preview-button onclick="configManager.previewVirtualFolder(${id})">
                            <i class="bi bi-eye" aria-hidden="true"></i> Preview matches
                        </button>
                    </div>
                    <div class="hidden rounded-box bg-base-200 p-4 text-sm" data-vf-preview-result role="status" aria-live="polite"></div>
                </div>
            </article>`;
        this.refs.virtualFoldersContainer.insertAdjacentHTML('beforeend', folderHtml);
        const folder = this.refs.virtualFoldersContainer.querySelector(`[data-virtual-folder="${id}"]`);
        if (shouldFocus) {
            folder?.querySelector('[data-vf-name]')?.focus();
            this.announceVirtualFolder(`Added ${title}.`);
        }
    }

    renderVirtualFolderEmptyState() {
        this.refs.virtualFoldersContainer.innerHTML = `<div class="rounded-box border border-dashed border-base-300 bg-base-100 p-8 text-center" data-vf-empty-state>
            <i class="bi bi-folder-plus text-3xl text-base-content/40" aria-hidden="true"></i>
            <p class="mt-3 font-medium">No virtual folders yet</p>
            <p class="mt-1 text-sm text-base-content/65">Add one to create a focused view such as 4K, Recently Added, or a provider-specific library.</p>
        </div>`;
    }

    virtualFolderNoConditions() {
        return `<div class="rounded-box border border-dashed border-base-300 p-4 text-sm text-base-content/70" data-vf-no-conditions>
            No conditions yet. This view will show every healthy item.
        </div>`;
    }

    addVirtualFolderCondition(folderId) {
        const container = document.getElementById(`virtual_folder_${folderId}_conditions`);
        if (!container) return;
        container.querySelector('[data-vf-no-conditions]')?.remove();
        const conditionId = this.virtualFolderConditionCounts[folderId]++;
        container.insertAdjacentHTML('beforeend', this.renderVirtualFolderCondition(folderId, conditionId));
        container.querySelector(`[data-vf-condition="${conditionId}"] [data-vf-field]`)?.focus();
        this.invalidateVirtualFolderPreview(container.closest('[data-virtual-folder]'));
        this.announceVirtualFolder('Condition added.');
    }

    applyVirtualFolderSample(folderId, sampleKey) {
        const sample = this.virtualFolderSamples.find(option => option.key === sampleKey);
        const container = document.getElementById(`virtual_folder_${folderId}_conditions`);
        if (!sample || !container) return;

        const blankRow = [...container.querySelectorAll('[data-vf-condition]')]
            .find(row => this.conditionFromRow(row).value === '');
        let conditionId;
        if (blankRow) {
            conditionId = Number(blankRow.dataset.vfCondition);
            blankRow.outerHTML = this.renderVirtualFolderCondition(folderId, conditionId, sample.condition);
        } else {
            container.querySelector('[data-vf-no-conditions]')?.remove();
            conditionId = this.virtualFolderConditionCounts[folderId]++;
            container.insertAdjacentHTML('beforeend', this.renderVirtualFolderCondition(folderId, conditionId, sample.condition));
        }

        const row = container.querySelector(`[data-vf-condition="${conditionId}"]`);
        row?.querySelector('[data-vf-value]')?.focus();
        this.invalidateVirtualFolderPreview(container.closest('[data-virtual-folder]'));
        this.announceVirtualFolder(`${sample.label} example applied. You can edit it before saving.`);
    }

    removeVirtualFolderCondition(folderId, conditionId) {
        const container = document.getElementById(`virtual_folder_${folderId}_conditions`);
        if (!container) return;
        container.querySelector(`[data-vf-condition="${conditionId}"]`)?.remove();
        if (!container.querySelector('[data-vf-condition]')) container.innerHTML = this.virtualFolderNoConditions();
        this.invalidateVirtualFolderPreview(container.closest('[data-virtual-folder]'));
        this.announceVirtualFolder('Condition removed.');
    }

    conditionFromRow(row) {
        return {
            field: row.querySelector('[data-vf-field]')?.value || 'entry_name',
            operator: row.querySelector('[data-vf-operator]')?.value || 'contains',
            value: (row.querySelector('[data-vf-value]')?.value || '').trim(),
            case_sensitive: Boolean(row.querySelector('[data-vf-case]')?.checked)
        };
    }

    refreshVirtualFolderCondition(row) {
        const folder = row.closest('[data-virtual-folder]');
        if (!folder) return;
        const folderId = Number(folder.dataset.virtualFolder);
        const conditionId = Number(row.dataset.vfCondition);
        const previousField = row.dataset.vfCurrentField;
        const condition = this.conditionFromRow(row);
        const field = this.virtualFolderField(condition.field);
        if (previousField !== condition.field) condition.value = '';
        if (!field.operators.some(([value]) => value === condition.operator)) condition.operator = field.operators[0][0];
        if (condition.field === 'protocol' && !['torrent', 'nzb'].includes(condition.value)) condition.value = 'torrent';
        if (condition.field === 'provider' && this.virtualFolderProviders.length && !this.virtualFolderProviders.includes(condition.value)) {
            condition.value = this.virtualFolderProviders[0];
        }
        row.outerHTML = this.renderVirtualFolderCondition(folderId, conditionId, condition);
        folder.querySelector(`[data-vf-condition="${conditionId}"] [data-vf-field]`)?.focus();
    }

    handleVirtualFolderInput(event) {
        const folder = event.target.closest('[data-virtual-folder]');
        if (!folder) return;
        if (event.type === 'change' && event.target.matches('[data-vf-field]')) {
            this.refreshVirtualFolderCondition(event.target.closest('[data-vf-condition]'));
        }
        if (event.target.matches('[data-vf-name]')) {
            const title = event.target.value.trim() || `New virtual folder ${Number(folder.dataset.virtualFolder) + 1}`;
            folder.querySelector('[data-vf-title]').textContent = title;
        }
        this.invalidateVirtualFolderPreview(folder);
    }

    invalidateVirtualFolderPreview(folder) {
        const result = folder?.querySelector('[data-vf-preview-result]');
        if (!result) return;
        result.classList.add('hidden');
        result.innerHTML = '';
    }

    removeVirtualFolder(id) {
        const folder = this.refs.virtualFoldersContainer.querySelector(`[data-virtual-folder="${id}"]`);
        if (!folder) return;
        const name = folder.querySelector('[data-vf-name]')?.value.trim() || 'this virtual folder';
        if (confirm(`Remove "${name}"?\n\nThis removes only the filtered view. Your media will not be deleted.`)) {
            folder.remove();
            delete this.virtualFolderConditionCounts[id];
            if (!this.refs.virtualFoldersContainer.querySelector('[data-virtual-folder]')) this.renderVirtualFolderEmptyState();
            this.announceVirtualFolder(`${name} removed. Save settings to apply this change.`);
        }
    }

    collectVirtualFolder(folderEl) {
        return {
            name: (folderEl.querySelector('[data-vf-name]')?.value || '').trim(),
            match: folderEl.querySelector('[data-vf-match]')?.value || 'all',
            include_bad: Boolean(folderEl.querySelector('[data-vf-include-bad]')?.checked),
            conditions: Array.from(folderEl.querySelectorAll('[data-vf-condition]')).map(row => this.conditionFromRow(row))
        };
    }

    collectVirtualFolders() {
        return Array.from(this.refs.virtualFoldersContainer.querySelectorAll('[data-virtual-folder]'))
            .map(folder => this.collectVirtualFolder(folder));
    }

    validateVirtualFolderDefinition(folder) {
        const errors = [];
        if (!folder.name) errors.push('Folder name is required.');
        if (folder.name === '.' || folder.name === '..' || /[<>:"/\\|?*\u0000-\u001f\u007f]/.test(folder.name)) {
            errors.push('Folder name contains characters that are not portable across mounts and shares.');
        }
        if (/\.$/.test(folder.name) || /^(?:CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(?:\..*)?$/i.test(folder.name)) {
            errors.push('Folder name is reserved by common filesystems.');
        }
        folder.conditions.forEach((condition, index) => {
            const label = `Condition ${index + 1}`;
            const field = this.virtualFolderField(condition.field);
            if (!field.operators.some(([value]) => value === condition.operator)) errors.push(`${label} has an unsupported rule.`);
            if (!condition.value) errors.push(`${label} needs a value.`);
            if (condition.field === 'size' && condition.value && !/^\d+(?:\.\d+)?\s*(?:B|KB|MB|GB|TB|PB)?$/i.test(condition.value)) {
                errors.push(`${label} needs a size such as 700MB or 20GB.`);
            }
            if (condition.field === 'added' && condition.value && !/^(?=.)(?:\d+w)?(?:\d+d)?(?:\d+(?:\.\d+)?(?:h|m|s))?$/.test(condition.value)) {
                errors.push(`${label} needs a duration such as 12h, 7d, or 2w.`);
            }
            if (condition.field === 'file_count' && condition.value && !/^\d+$/.test(condition.value)) {
                errors.push(`${label} needs a whole number of files.`);
            }
            if (['matches_regex', 'not_matches_regex'].includes(condition.operator) && condition.value) {
                try { new RegExp(condition.value); } catch (_) { errors.push(`${label} contains an invalid regular expression.`); }
            }
        });
        return errors;
    }

    async previewVirtualFolder(id) {
        const folderEl = this.refs.virtualFoldersContainer.querySelector(`[data-virtual-folder="${id}"]`);
        if (!folderEl) return;
        const folder = this.collectVirtualFolder(folderEl);
        const result = folderEl.querySelector('[data-vf-preview-result]');
        const button = folderEl.querySelector('[data-vf-preview-button]');
        const errors = this.validateVirtualFolderDefinition(folder);
        result.classList.remove('hidden');
        if (errors.length) {
            result.innerHTML = `<p class="font-medium text-error">Fix this folder before previewing:</p><ul class="mt-2 list-disc pl-5">${errors.map(error => `<li>${window.decypharrUtils.escapeHtml(error)}</li>`).join('')}</ul>`;
            return;
        }

        button.disabled = true;
        button.setAttribute('aria-busy', 'true');
        result.textContent = 'Checking your library…';
        try {
            const response = await window.decypharrUtils.fetcher('/api/virtual-folders/preview', {
                method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({folder, limit: 5})
            });
            if (!response.ok) throw new Error((await response.text()).trim() || 'Preview failed');
            const preview = await response.json();
            const samples = Array.isArray(preview.samples) ? preview.samples : [];
            result.innerHTML = `<p class="font-semibold">${preview.total} matching item${preview.total === 1 ? '' : 's'}</p>
                ${samples.length ? `<p class="mt-1 text-xs text-base-content/65">A few examples:</p><ul class="mt-2 space-y-1">${samples.map(item => `<li class="flex flex-wrap justify-between gap-2"><span>${window.decypharrUtils.escapeHtml(item.name)}</span><span class="text-xs text-base-content/60">${window.decypharrUtils.escapeHtml(item.provider || item.protocol || '')}${item.size ? ` · ${window.decypharrUtils.formatBytes(item.size)}` : ''}</span></li>`).join('')}</ul>` : '<p class="mt-1 text-base-content/65">No current item matches these conditions.</p>'}`;
        } catch (error) {
            result.innerHTML = `<p class="text-error">Could not preview: ${window.decypharrUtils.escapeHtml(error.message)}</p>`;
        } finally {
            button.disabled = false;
            button.removeAttribute('aria-busy');
        }
    }

    announceVirtualFolder(message) {
        const status = document.getElementById('virtualFolderEditorStatus');
        if (status) status.textContent = message;
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
            'processing_max_connections': usenet.processing_max_connections,
            'read_ahead': usenet.read_ahead,
            'processing_timeout': usenet.processing_timeout,
            'conn_idle_timeout': usenet.conn_idle_timeout,
            'availability_sample_percent': usenet.availability_sample_percent,
            'import_availability_sample_percent': usenet.import_availability_sample_percent,
            'disk_path': usenet.disk_path,
            'buffer_memory': usenet.buffer_memory
        };

        Object.entries(streamFields).forEach(([id, value]) => {
            const input = document.getElementsByName(`usenet.${id}`)[0];
            if (input && value !== undefined) {
                if (input.type === 'checkbox') {
                    input.checked = !!value;
                } else {
                    input.value = value;
                }
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
                <div class="mb-4 flex items-start justify-between gap-3">
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

                <div class="flex flex-wrap gap-4 mt-4">
                    <label class="flex items-center gap-2 cursor-pointer">
                        <input type="checkbox" class="checkbox checkbox-primary checkbox-sm"
                               name="usenet.providers[${index}].ssl"
                               id="usenet_provider_${index}_ssl">
                        <span class="text-sm">Use SSL</span>
                    </label>
                    <label class="flex items-center gap-2 cursor-pointer"
                           title="Only used when every non-backup provider is excluded (article not found, connection errors). Not used just because primary pools are busy — requests wait for a primary slot instead. Use this for block providers you only want to bill for completion.">
                        <input type="checkbox" class="checkbox checkbox-warning checkbox-sm"
                               name="usenet.providers[${index}].backup"
                               id="usenet_provider_${index}_backup">
                        <span class="text-sm">Backup provider (fallback only)</span>
                    </label>
                </div>
            </div>
        </div>
        `;
    }
}
